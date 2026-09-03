import { AsyncLocalStorage } from 'node:async_hooks'

import type {
  ChannelConnectorDelivery,
  ChannelConnectorRuntimeUnit,
  ChannelInboundEventRequest,
  InteractionForm,
} from '@omnara/sdk'
import {
  type Adapter,
  Chat,
  ChatError,
  type ConcurrencyConfig,
  type ConcurrencyStrategy,
  type Message,
  type MessageContext,
  RateLimitError,
  type StateAdapter,
  type Thread,
} from 'chat'

import { createChatSdkLogger } from './chat-sdk-logger'
import { type ChatSdkAttachmentDataLoader, messageContentBlocks } from './chat-sdk-media'
import { errorMessage } from './diagnostics'
import {
  type GatewayLogger,
  ProviderDeliveryError,
  type ProviderInboundContext,
  type ProviderRuntime,
  type ProviderWorkReservation,
  type RuntimeUnitContext,
} from './types'

const maxOutboundChannelMessageTextBytes = 64 * 1024

export interface ChatSdkEnvelopeIdentity {
  actorDisplayName: string
  actorMetadata?: Record<string, unknown>
  actorRef: string
  conversationDisplayName?: string
  conversationKind: string
  conversationMetadata?: Record<string, unknown>
  eventMetadata?: Record<string, unknown>
  eventType: string
  externalAccountRef: string
  externalTenantId: string
  providerEventId: string
}

export interface ChatSdkRuntimeOptions {
  adapter: Adapter
  configure?: (chat: Chat, inbound: ChatSdkInboundActions) => Promise<void> | void
  concurrency?: ConcurrencyStrategy | ConcurrencyConfig
  deliveryHandlers?: ReadonlyMap<string, ChatSdkDeliveryHandler>
  fetchAttachmentData?: ChatSdkAttachmentDataLoader
  initializationCleanupTimeoutMs?: number
  integrationAppId: string
  logger: GatewayLogger
  mediaFetchTimeoutMs?: number
  maxMediaItemBytes?: number
  maxMediaTotalBytes?: number
  resolveIdentity(thread: Thread, message: Message): Promise<ChatSdkEnvelopeIdentity>
  runUnit?: (
    chat: Chat,
    unit: ChannelConnectorRuntimeUnit,
    context: RuntimeUnitContext,
  ) => Promise<void>
  signal: AbortSignal
  state: StateAdapter
  userName: string
}

export type ChatSdkInboundActions = ProviderInboundContext

interface ChatSdkEventContext extends ProviderInboundContext {
  reserveWorkBytes(bytes: number): ProviderWorkReservation
}

export type ChatSdkDeliveryHandler = (
  chat: Chat,
  delivery: ChannelConnectorDelivery,
  context: Parameters<ProviderRuntime['send']>[1],
) => Promise<Awaited<ReturnType<ProviderRuntime['send']>>>

export function chatSdkDeliveryHandlerKey(deliveryKind: string, payloadVersion: string): string {
  return `${deliveryKind}\u0000${payloadVersion}`
}

export async function createChatSdkRuntime(
  options: ChatSdkRuntimeOptions,
): Promise<ProviderRuntime> {
  const inboundScopes = new AsyncLocalStorage<{
    context: ChatSdkEventContext
    signal: AbortSignal
  }>()
  const inbound: ChatSdkInboundActions = {
    resolveInteraction: (interactionId, request) =>
      requireInboundScope(inboundScopes).context.resolveInteraction(interactionId, request),
    submitInbound: (event) => requireInboundScope(inboundScopes).context.submitInbound(event),
  }
  const chat = new Chat({
    adapters: { [options.adapter.name]: options.adapter },
    concurrency: options.concurrency ?? 'concurrent',
    logger: createChatSdkLogger(options.logger, options.integrationAppId),
    state: options.state,
    userName: options.userName,
  })
  const receive = async (thread: Thread, message: Message): Promise<void> => {
    const inboundScope = requireInboundScope(inboundScopes)
    const reservations: ProviderWorkReservation[] = []
    const reserveWorkBytes = (bytes: number): ProviderWorkReservation => {
      const reservation = inboundScope.context.reserveWorkBytes(bytes)
      reservations.push(reservation)
      return reservation
    }
    try {
      const identity = await options.resolveIdentity(thread, message)
      const contentBlocks = await messageContentBlocks(
        message,
        options.maxMediaItemBytes ?? 10 * 1024 * 1024,
        options.maxMediaTotalBytes ?? 24 * 1024 * 1024,
        {
          fetchAttachmentData: options.fetchAttachmentData,
          fetchTimeoutMs: options.mediaFetchTimeoutMs,
          reserveWorkBytes,
          signal: inboundScope.signal,
        },
      )
      const event: ChannelInboundEventRequest = {
        actor: {
          display_name: identity.actorDisplayName,
          metadata: identity.actorMetadata ?? {},
          ref: identity.actorRef,
        },
        content_blocks: contentBlocks,
        conversation: {
          direct: thread.isDM,
          display_name: identity.conversationDisplayName,
          kind: identity.conversationKind,
          mentioned: message.isMention ?? false,
          metadata: identity.conversationMetadata ?? {},
          parent_ref: thread.channelId,
          ref: thread.id,
          reply_to_ref: message.replyTo?.id,
        },
        event_type: identity.eventType,
        external_account_ref: identity.externalAccountRef,
        external_tenant_id: identity.externalTenantId,
        metadata: identity.eventMetadata ?? {},
        occurred_at: message.metadata.dateSent.toISOString(),
        provider_event_id: identity.providerEventId,
        version: 'v1',
      }
      try {
        await inbound.submitInbound(event)
      } catch (error) {
        options.logger.error('submit Chat SDK inbound event', {
          error: errorMessage(error),
          integration_app_id: options.integrationAppId,
          provider_event_id: identity.providerEventId,
        })
        throw error
      }
    } finally {
      for (const reservation of reservations.reverse()) reservation.release()
    }
  }
  const receiveQueued = async (
    thread: Thread,
    message: Message,
    context?: MessageContext,
  ): Promise<void> => {
    for (const item of [...(context?.skipped ?? []), message]) await receive(thread, item)
  }
  chat.onDirectMessage(async (thread, message, _channel, context) => {
    await receiveQueued(thread, message, context)
  })
  chat.onNewMention(receiveQueued)
  chat.onSubscribedMessage(receiveQueued)
  chat.onNewMessage(/[\s\S]*/, receiveQueued)
  let initializationStep: Promise<unknown> | undefined
  try {
    initializationStep = Promise.resolve(options.configure?.(chat, inbound))
    await raceWithSignal(initializationStep, options.signal)
    initializationStep = chat.initialize()
    await raceWithSignal(initializationStep, options.signal)
  } catch (error) {
    await shutdownAfterInitializationFailure(chat, options)
    if (error instanceof LifecycleCancellation && initializationStep) {
      void initializationStep.then(
        () => shutdownAfterInitializationFailure(chat, options),
        () => shutdownAfterInitializationFailure(chat, options),
      )
      throw error.reason
    }
    throw error
  }

  const runtime: ProviderRuntime = {
    close: () => chat.shutdown(),
    handleWebhook: async (request, context) => {
      const handler = chat.webhooks[options.adapter.name]
      if (!handler) return new Response('provider adapter is not registered', { status: 404 })
      return inboundScopes.run({ context, signal: request.signal }, () =>
        handler(request, {
          waitUntil: (task) => {
            context.waitUntil(task)
          },
        }),
      )
    },
    send: async (delivery, context) => {
      try {
        if (context.signal.aborted) {
          throw new ProviderDeliveryError('channel delivery was canceled before send', {
            retryable: true,
          })
        }
        const handler = options.deliveryHandlers?.get(
          chatSdkDeliveryHandlerKey(delivery.delivery_kind, delivery.payload_version),
        )
        if (handler) return await handler(chat, delivery, context)
        throw new ProviderDeliveryError(
          `no abort-aware Chat SDK delivery handler is registered for ${delivery.delivery_kind}/${delivery.payload_version}`,
        )
      } catch (error) {
        throw normalizeProviderDeliveryError(error)
      }
    },
  }
  const runUnit = options.runUnit
  if (runUnit) {
    runtime.runUnit = (unit, context) =>
      inboundScopes.run({ context, signal: context.signal }, () => runUnit(chat, unit, context))
  }
  return runtime
}

function requireInboundScope(
  scopes: AsyncLocalStorage<{ context: ChatSdkEventContext; signal: AbortSignal }>,
): { context: ChatSdkEventContext; signal: AbortSignal } {
  const scope = scopes.getStore()
  if (!scope) throw new Error('Chat SDK inbound work requires an active webhook or runtime unit')
  if (scope.signal.aborted) {
    throw scope.signal.reason instanceof Error
      ? scope.signal.reason
      : new Error('Chat SDK inbound work context is no longer active')
  }
  return scope
}

async function shutdownAfterInitializationFailure(
  chat: Chat,
  options: ChatSdkRuntimeOptions,
): Promise<void> {
  const timeoutMs = options.initializationCleanupTimeoutMs ?? 10_000
  try {
    await raceWithSignal(chat.shutdown(), AbortSignal.timeout(timeoutMs))
  } catch (error) {
    options.logger.error('clean up failed Chat SDK initialization', {
      error: errorMessage(error),
    })
  }
}

async function raceWithSignal<T>(work: Promise<T>, signal?: AbortSignal): Promise<T> {
  if (!signal) return work
  if (signal.aborted) throw new LifecycleCancellation(signal.reason)
  return new Promise<T>((resolve, reject) => {
    const onAbort = (): void => {
      reject(new LifecycleCancellation(signal.reason))
    }
    signal.addEventListener('abort', onAbort, { once: true })
    work.then(
      (value) => {
        signal.removeEventListener('abort', onAbort)
        resolve(value)
      },
      (error: unknown) => {
        signal.removeEventListener('abort', onAbort)
        reject(error instanceof Error ? error : new Error(String(error)))
      },
    )
  })
}

class LifecycleCancellation extends Error {
  constructor(readonly reason: unknown) {
    super('Chat SDK lifecycle operation was canceled')
  }
}

export function normalizeProviderDeliveryError(error: unknown): ProviderDeliveryError {
  if (error instanceof ProviderDeliveryError) return error
  if (error instanceof RateLimitError) {
    return new ProviderDeliveryError(error.message, {
      retryAfterMs: error.retryAfterMs,
      retryable: true,
    })
  }
  if (error instanceof ChatError || isAdapterError(error)) {
    const code = error.code
    if (code === 'RATE_LIMITED') {
      return new ProviderDeliveryError(errorMessage(error), {
        retryAfterMs: adapterRetryAfterMs(error),
        retryable: true,
      })
    }
    if (code === 'NETWORK_ERROR') {
      return new ProviderDeliveryError(errorMessage(error), { outcomeUnknown: true })
    }
    return new ProviderDeliveryError(errorMessage(error))
  }
  return new ProviderDeliveryError(errorMessage(error), { outcomeUnknown: true })
}

export interface ChannelDeliveryDestination {
  channel_id: string
  provider_metadata: Record<string, unknown>
  provider_ref: string
  provider_ref_kind: string
}

export interface ChannelMessagePayload {
  destination: {
    channel_id: string
    provider_metadata: Record<string, unknown>
    provider_ref: string
    provider_ref_kind: string
  }
  context: { agent_id: string; provider_call_id: string }
  message: { text: string }
}

export function parseChannelMessageDelivery(
  delivery: Parameters<ProviderRuntime['send']>[0],
): ChannelMessagePayload {
  if (delivery.delivery_kind !== 'message' || delivery.payload_version !== 'channel-message.v1') {
    throw malformedDelivery('channel delivery kind or payload version is unsupported')
  }
  const value = delivery.payload
  if (!isRecord(value.destination) || !isRecord(value.message) || !isRecord(value.context)) {
    throw malformedDelivery('channel delivery payload is malformed')
  }
  const destination = parseDeliveryDestination(delivery, value.destination)
  const { text } = value.message
  const { agent_id: agentId, provider_call_id: providerCallId } = value.context
  if (
    typeof text !== 'string' ||
    text.trim() === '' ||
    Buffer.byteLength(text, 'utf8') > maxOutboundChannelMessageTextBytes ||
    typeof agentId !== 'string' ||
    agentId === '' ||
    typeof providerCallId !== 'string' ||
    providerCallId === ''
  ) {
    throw malformedDelivery('channel delivery payload is malformed')
  }
  return {
    context: { agent_id: agentId, provider_call_id: providerCallId },
    destination,
    message: { text },
  }
}

export interface ChannelInteractionPromptPayload {
  context: { agent_id: string; provider_call_id: string }
  destination: ChannelDeliveryDestination
  interaction: { form: InteractionForm; id: string; kind: 'permission' | 'question' }
}

export function parseChannelInteractionPromptDelivery(
  delivery: ChannelConnectorDelivery,
): ChannelInteractionPromptPayload {
  if (
    delivery.delivery_kind !== 'interaction' ||
    delivery.payload_version !== 'channel-interaction.v1'
  ) {
    throw malformedDelivery('channel interaction delivery kind or payload version is unsupported')
  }
  const value = delivery.payload
  if (!isRecord(value.destination) || !isRecord(value.interaction) || !isRecord(value.context)) {
    throw malformedDelivery('channel interaction delivery payload is malformed')
  }
  const destination = parseDeliveryDestination(delivery, value.destination)
  const { id, kind, form } = value.interaction
  const { agent_id: agentId, provider_call_id: providerCallId } = value.context
  if (
    typeof id !== 'string' ||
    !id.startsWith('aint_') ||
    (kind !== 'permission' && kind !== 'question') ||
    !isInteractionForm(form) ||
    typeof agentId !== 'string' ||
    agentId === '' ||
    typeof providerCallId !== 'string' ||
    providerCallId === ''
  ) {
    throw malformedDelivery('channel interaction delivery payload is malformed')
  }
  return {
    context: { agent_id: agentId, provider_call_id: providerCallId },
    destination,
    interaction: { form, id, kind },
  }
}

function parseDeliveryDestination(
  delivery: ChannelConnectorDelivery,
  value: Record<string, unknown>,
): ChannelDeliveryDestination {
  const {
    channel_id: channelId,
    provider_metadata: providerMetadata,
    provider_ref: providerRef,
    provider_ref_kind: providerRefKind,
  } = value
  if (
    typeof channelId !== 'string' ||
    channelId !== delivery.integration_target_id ||
    typeof providerRef !== 'string' ||
    providerRef === '' ||
    typeof providerRefKind !== 'string' ||
    providerRefKind === '' ||
    !isRecord(providerMetadata)
  ) {
    throw malformedDelivery('channel delivery destination is malformed')
  }
  return {
    channel_id: channelId,
    provider_metadata: providerMetadata,
    provider_ref: providerRef,
    provider_ref_kind: providerRefKind,
  }
}

function isInteractionForm(value: unknown): value is InteractionForm {
  if (!isRecord(value) || typeof value.title !== 'string' || !Array.isArray(value.questions)) {
    return false
  }
  return (
    value.title.trim() !== '' &&
    value.questions.length > 0 &&
    value.questions.every(
      (question) =>
        isRecord(question) &&
        typeof question.prompt === 'string' &&
        question.prompt.trim() !== '' &&
        Array.isArray(question.options) &&
        question.options.length > 0 &&
        question.options.every(
          (option) =>
            isRecord(option) && typeof option.label === 'string' && option.label.trim() !== '',
        ),
    )
  )
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isAdapterError(error: unknown): error is { code: string; message?: string } {
  return isRecord(error) && typeof error.code === 'string'
}

function adapterRetryAfterMs(error: { code: string }): number | undefined {
  if (!('retryAfterMs' in error)) return undefined
  const value = error.retryAfterMs
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : undefined
}

function malformedDelivery(message: string): ProviderDeliveryError {
  return new ProviderDeliveryError(message)
}

export type { ChatSdkAttachmentDataLoader, ChatSdkAttachmentLoadContext } from './chat-sdk-media'
export { fetchBoundedMedia, messageContentBlocks } from './chat-sdk-media'
