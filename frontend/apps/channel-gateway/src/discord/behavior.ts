import {
  ApiError,
  type ChannelConnectorEventReceipt,
  type ChannelDefinition,
  type ChannelRegistrationTarget,
  type CreateAgentInputContentBlock,
  schemas,
} from '@omnara/sdk'

import type { CoreClient, ReceiptInputRequest } from '../core/client'
import { ReceiptClientError } from '../core/receipt-http'
import { isTransientCoreError } from '../core/requests'
import {
  type OperationAttemptContext,
  OperationRetryError,
  retryOperation,
} from '../operations/retry'
import {
  type ProviderWorkReservation,
  type ReceiptBehaviorContext,
  ReceiptBehaviorError,
} from '../types'
import { GatewayAtCapacityError } from '../work-budget'
import { DiscordAddressError, discordDefinition, loadDiscordAddress } from './address'
import { prepareDiscordFiles } from './behavior-files'
import { discordInputTargets, ensureDiscordMentionThread } from './behavior-routing'
import { DiscordClient } from './client'
import { discordConfiguration } from './configuration'
import { discordInputKey, discordInputText, parseDiscordReceipt } from './events'
import { DiscordAPIError } from './protocol'

export interface DiscordBehaviorContext extends ReceiptBehaviorContext {
  core: Pick<
    CoreClient,
    | 'getAppConfiguration'
    | 'getInstallationConfiguration'
    | 'listRoutes'
    | 'publishDefinition'
    | 'lookupRecipients'
    | 'deliverInput'
    | 'lookupWorkflow'
    | 'deliverWorkflow'
  >
  reserveWorkBytes: (bytes: number) => ProviderWorkReservation
  /** Operator/test override, never provided by a receipt or route. */
  apiUrl?: string
}

/** Verified raw Discord Dispatch -> existing atomic receipt admission APIs. */
export async function processDiscordEvent(
  receipt: Readonly<ChannelConnectorEventReceipt>,
  context: DiscordBehaviorContext,
): Promise<void> {
  const deadlineMs = Math.min(context.deadlineMs, Date.parse(receipt.lease_expires_at))
  const remaining = deadlineMs - Date.now()
  if (!Number.isSafeInteger(remaining) || remaining <= 0 || remaining > 2_147_483_647)
    throw new ReceiptBehaviorError(true)
  const lifetime = new AbortController()
  const timer = setTimeout(() => {
    lifetime.abort()
  }, remaining)
  const signal = AbortSignal.any([context.signal, lifetime.signal])
  let work: ProviderWorkReservation | undefined
  try {
    signal.throwIfAborted()
    const event = parseDiscordReceipt(receipt)
    if (!event) return
    const app = await context.core.getAppConfiguration(receipt.integration_app_id, signal)
    const install = await context.core.getInstallationConfiguration(
      receipt.integration_app_id,
      receipt.integration_install_id,
      signal,
    )
    if (
      !schemas.zChannelConnectorAppConfiguration.safeParse(app).success ||
      !schemas.zChannelConnectorInstallationConfiguration.safeParse(install).success ||
      app.app.id !== receipt.integration_app_id ||
      install.install.id !== receipt.integration_install_id
    )
      throw new DiscordAPIError('event_scope_mismatch')
    const configuration = discordConfiguration(app, install)
    if (event.guild_id !== configuration.guildID) throw new DiscordAPIError('event_scope_mismatch')
    if (event.author.id === configuration.botUserID) return
    const client = new DiscordClient(configuration, context.apiUrl)
    const attempt: OperationAttemptContext = {
      requestId: receipt.receipt_id,
      deadlineMs,
      signal,
      attempt: 1,
    }
    const loadSource = () =>
      retryOperation({ ...attempt, idempotent: true }, async (readAttempt) => {
        try {
          return await loadDiscordAddress(client, event.channel_id, undefined, readAttempt)
        } catch (error) {
          if (error instanceof DiscordAddressError && error.code === 'unsupported_address')
            return undefined
          throw error
        }
      })
    const mentioned = event.mentions.some((user) => user.id === configuration.botUserID)
    // Unmentioned input can only continue a registered thread. Consult core
    // first; irrelevant roots/unknown threads need no provider reads. Actionable
    // receipts still verify this credential's identity and native guild address.
    let source = mentioned ? await loadSource() : undefined
    if (mentioned && !source) return
    const providerRef = source?.kind === 'channel' ? event.id : event.channel_id
    const inputKey = discordInputKey(event)
    const text = discordInputText(event, providerRef)
    let files: CreateAgentInputContentBlock[] | undefined
    let thread = source?.kind === 'thread' ? source.channel : undefined
    let definition: ChannelDefinition | undefined
    let parentDefinition: ChannelDefinition | undefined
    const completed = new Set<string>()
    const failedPresentations = new Set<string>()
    for (let render = 0; render < 4; render++) {
      let retry = false
      for await (const target of discordInputTargets(
        receipt,
        context,
        providerRef,
        inputKey,
        mentioned,
        signal,
      )) {
        signal.throwIfAborted()
        source ??= await loadSource()
        if (!source || (!mentioned && source.kind === 'channel')) return
        thread ??= source.kind === 'thread' ? source.channel : undefined
        const identity =
          target.kind === 'recipient'
            ? `agent:${target.recipient.agent_id}`
            : `route:${target.routeID}`
        if (completed.has(identity)) continue
        const keys =
          target.kind === 'recipient' ? target.recipient.input_keys : target.lookup.input_keys
        const replay = keys.includes(inputKey)
        const presentation = JSON.stringify([
          identity,
          target.kind === 'recipient' ? target.recipient.binding_id : target.lookup.exists,
          replay,
        ])
        if (failedPresentations.has(presentation)) throw new ReceiptClientError('http_error', 409)
        if (!replay && event.attachments.length && !files) {
          work ??= context.reserveWorkBytes(0)
          files = await prepareDiscordFiles(event, configuration, attempt, work, context.apiUrl)
        }
        const body: Omit<ReceiptInputRequest, 'binding_id'> = {
          input_key: inputKey,
          author: {
            ref: event.author.id,
            display_name: event.author.global_name ?? event.author.username,
          },
          content_blocks: replay ? text : [...text, ...(files ?? [])],
          metadata: {
            provider: 'discord',
            message_id: event.id,
            guild_id: configuration.guildID,
            channel_id: event.channel_id,
            provider_ref: providerRef,
          },
          delivery_mode: 'steering',
          cancel_open_interactions: true,
        }
        try {
          if (target.kind === 'recipient') {
            await context.core.deliverInput(
              receipt,
              { ...body, binding_id: target.recipient.binding_id },
              signal,
            )
          } else {
            if (!thread && !replay)
              thread = await ensureDiscordMentionThread(client, event, attempt)
            definition ??= await context.core.publishDefinition(
              receipt,
              discordDefinition('thread'),
              signal,
            )
            const address: ChannelRegistrationTarget = {
              definition_id: definition.id,
              provider_ref: providerRef,
              provider_ref_kind: 'thread',
              display_name: thread?.name,
              parent_channel_id: target.parentChannelID,
            }
            if (!target.channelID) {
              const parent = source.parent ?? source.channel
              parentDefinition ??= await context.core.publishDefinition(
                receipt,
                discordDefinition('channel'),
                signal,
              )
              address.parent = {
                definition_id: parentDefinition.id,
                provider_ref: parent.id,
                provider_ref_kind: 'channel',
                display_name: parent.name,
              }
            }
            await context.core.deliverWorkflow(
              receipt,
              {
                ...body,
                route_id: target.routeID,
                instance_key: providerRef,
                only_if_unbound: target.lookup.exists ? undefined : true,
                target: address,
                grants: { read: true, send: true },
              },
              signal,
            )
          }
          completed.add(identity)
        } catch (error) {
          if (
            !(error instanceof ReceiptClientError) ||
            error.apiCode === 'managed_work_admission_denied' ||
            error.code !== 'http_error' ||
            (error.status !== 409 && !(target.kind === 'recipient' && error.status === 404)) ||
            render === 3
          )
            throw error
          failedPresentations.add(presentation)
          retry = true
          break
        }
      }
      if (!retry) return
    }
  } catch (cause) {
    if (signal.aborted || cause instanceof GatewayAtCapacityError || isTransientCoreError(cause))
      throw new ReceiptBehaviorError(true)
    if (cause instanceof DiscordAPIError)
      // The only mutation is one thread per source message; replay reads its
      // deterministic address first, including after an ambiguous creation.
      throw new ReceiptBehaviorError(cause.retryable || cause.outcomeUnknown)
    if (cause instanceof OperationRetryError)
      throw new ReceiptBehaviorError(
        ['retries_exhausted', 'deadline_exceeded', 'canceled'].includes(cause.code),
      )
    if (cause instanceof ReceiptClientError)
      throw new ReceiptBehaviorError(
        cause.apiCode !== 'managed_work_admission_denied' &&
          (cause.code === 'transport_failed' ||
            cause.code === 'aborted' ||
            (cause.code === 'http_error' &&
              (cause.status === 409 || cause.status === 429 || (cause.status ?? 0) >= 500))),
      )
    if (cause instanceof ApiError) throw new ReceiptBehaviorError(cause.status === 409)
    throw cause
  } finally {
    work?.release()
    clearTimeout(timer)
    lifetime.abort()
  }
}
