import type {
  ChannelConnectorDelivery,
  ChannelConnectorInstallationConfiguration,
  ChannelInboundEventRequest,
} from '@omnara/sdk'
import {
  type Adapter,
  type Chat,
  ChatError,
  type Message,
  RateLimitError,
  type StateAdapter,
} from 'chat'
import { describe, expect, it, vi } from 'vitest'

import { createChatSdkLogger } from './chat-sdk-logger'
import {
  chatSdkDeliveryHandlerKey,
  type ChatSdkInboundActions,
  createChatSdkRuntime,
  fetchBoundedMedia,
  messageContentBlocks,
  normalizeProviderDeliveryError,
  parseChannelInteractionPromptDelivery,
  parseChannelMessageDelivery,
} from './chat-sdk-runtime'
import { maxDiagnosticMessageBytes } from './diagnostics'

describe('Chat SDK delivery contracts', () => {
  it('bridges safe Chat SDK diagnostics into app-scoped gateway logs', () => {
    const logger = {
      debug: vi.fn(),
      error: vi.fn(),
      info: vi.fn(),
      warn: vi.fn(),
    }
    const chatLogger = createChatSdkLogger(logger, 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa')

    chatLogger.debug('Incoming message', { text: 'private message' })
    chatLogger.info('message-queued', { threadId: 'provider-thread-1' })
    chatLogger.child('discord').warn('Identity resolver failed', {
      error: new Error('lookup unavailable'),
      text: 'private message',
    })
    chatLogger.error('Adapter disconnect failed', new Error('socket closed'))
    chatLogger.error(
      'Oversized direct error',
      new Error(`before\u0000${'界'.repeat(maxDiagnosticMessageBytes)}`),
    )

    expect(logger.debug).not.toHaveBeenCalled()
    expect(logger.info).toHaveBeenCalledWith('message-queued', {
      integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      source: 'chat_sdk',
    })
    expect(logger.warn).toHaveBeenCalledWith('Identity resolver failed', {
      chat_sdk_scope: 'discord',
      error: 'lookup unavailable',
      integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      source: 'chat_sdk',
    })
    expect(logger.error).toHaveBeenCalledWith('Adapter disconnect failed', {
      error: 'socket closed',
      integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      source: 'chat_sdk',
    })
    const oversizedFields = logger.error.mock.calls[1]?.[1] as Record<string, unknown>
    expect(oversizedFields.error).not.toContain('\u0000')
    expect(Buffer.byteLength(oversizedFields.error as string)).toBeLessThanOrEqual(
      maxDiagnosticMessageBytes,
    )
  })

  it('constructs and shuts down a real Chat SDK runtime around a provider adapter', async () => {
    let inboundActions!: ChatSdkInboundActions
    const initialize = vi.fn(() => Promise.resolve())
    const disconnect = vi.fn(() => Promise.resolve())
    const handleWebhook = vi.fn(() => Promise.resolve(new Response('accepted', { status: 202 })))
    const stateConnect = vi.fn(() => Promise.resolve())
    const stateDisconnect = vi.fn(() => Promise.resolve())
    const adapter = {
      disconnect,
      handleWebhook,
      initialize,
      name: 'test',
    } as unknown as Adapter
    const state = {
      connect: stateConnect,
      disconnect: stateDisconnect,
    } as unknown as StateAdapter

    const runtime = await createChatSdkRuntime({
      adapter,
      configure: (chat, inbound) => {
        inboundActions = inbound
        expect((chat as unknown as { _concurrencyStrategy: string })._concurrencyStrategy).toBe(
          'concurrent',
        )
      },
      integrationAppId: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      logger: noopLogger,
      resolveIdentity: () => Promise.reject(new Error('unexpected message')),
      signal: new AbortController().signal,
      state,
      userName: 'omnara-test',
    })
    const response = await runtime.handleWebhook(new Request('https://example.test/webhook'), {
      ...unexpectedInboundContext,
      reserveWorkBytes: noopWorkReservation,
      waitUntil: () => undefined,
    })

    expect(response.status).toBe(202)
    expect(initialize).toHaveBeenCalledOnce()
    expect(stateConnect).toHaveBeenCalledOnce()
    expect(() => inboundActions.submitInbound(testInboundEvent)).toThrow(
      'requires an active webhook or runtime unit',
    )
    await expect(
      runtime.send(
        testDelivery({
          delivery_kind: 'message',
          payload: {
            context: { agent_id: 'agt_aaaaaaaaaaaaaaaaaaaaaaaaaa', provider_call_id: 'call-1' },
            destination: {
              channel_id: 'itgt_aaaaaaaaaaaaaaaaaaaaaaaaaa',
              provider_metadata: {},
              provider_ref: 'thread-1',
              provider_ref_kind: 'thread',
            },
            message: { text: 'hello' },
          },
          payload_version: 'channel-message.v1',
        }),
        { installation: testInstallation, signal: new AbortController().signal },
      ),
    ).rejects.toMatchObject({ outcomeUnknown: false, retryable: false })
    await runtime.close()
    expect(disconnect).toHaveBeenCalledOnce()
    expect(stateDisconnect).toHaveBeenCalledOnce()
  })

  it('disconnects partially initialized Chat SDK resources before surfacing failure', async () => {
    const initialize = vi.fn(() => Promise.reject(new Error('adapter initialization failed')))
    const disconnect = vi.fn(() => Promise.resolve())
    const stateConnect = vi.fn(() => Promise.resolve())
    const stateDisconnect = vi.fn(() => Promise.resolve())

    await expect(
      createChatSdkRuntime({
        adapter: {
          disconnect,
          handleWebhook: vi.fn(),
          initialize,
          name: 'test',
        } as unknown as Adapter,
        initializationCleanupTimeoutMs: 100,
        integrationAppId: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
        logger: noopLogger,
        resolveIdentity: () => Promise.reject(new Error('unexpected message')),
        signal: new AbortController().signal,
        state: {
          connect: stateConnect,
          disconnect: stateDisconnect,
        } as unknown as StateAdapter,
        userName: 'omnara-test',
      }),
    ).rejects.toThrow('adapter initialization failed')

    expect(initialize).toHaveBeenCalledOnce()
    expect(stateConnect).toHaveBeenCalledOnce()
    expect(disconnect).toHaveBeenCalledOnce()
    expect(stateDisconnect).toHaveBeenCalledOnce()
  })

  it('shuts down again if canceled initialization completes after the first cleanup', async () => {
    const initialization = deferred()
    const controller = new AbortController()
    const disconnect = vi.fn(() => Promise.resolve())
    const initialize = vi.fn(() => initialization.promise)
    const stateDisconnect = vi.fn(() => Promise.resolve())
    const creating = createChatSdkRuntime({
      adapter: {
        disconnect,
        handleWebhook: vi.fn(),
        initialize,
        name: 'test',
      } as unknown as Adapter,
      initializationCleanupTimeoutMs: 100,
      integrationAppId: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      logger: noopLogger,
      resolveIdentity: () => Promise.reject(new Error('unexpected message')),
      signal: controller.signal,
      state: {
        connect: vi.fn(() => Promise.resolve()),
        disconnect: stateDisconnect,
      } as unknown as StateAdapter,
      userName: 'omnara-test',
    })
    await vi.waitFor(() => {
      expect(initialize).toHaveBeenCalledOnce()
    })

    controller.abort(new Error('factory creation timed out'))
    await expect(creating).rejects.toThrow('factory creation timed out')
    expect(disconnect).toHaveBeenCalledOnce()
    expect(stateDisconnect).toHaveBeenCalledOnce()

    initialization.resolve()
    await vi.waitFor(() => {
      expect(disconnect).toHaveBeenCalledTimes(2)
      expect(stateDisconnect).toHaveBeenCalledTimes(2)
    })
  })

  it('logs an inbound submission failure before Chat SDK consumes its tracked task', async () => {
    let chat: Chat | undefined
    const tracked: Promise<unknown>[] = []
    const logger = { ...noopLogger, error: vi.fn() }
    const state = {
      connect: vi.fn(() => Promise.resolve()),
      disconnect: vi.fn(() => Promise.resolve()),
      isSubscribed: vi.fn(() => Promise.resolve(false)),
      setIfNotExists: vi.fn(() => Promise.resolve(true)),
    } as unknown as StateAdapter
    const message = {
      attachments: [],
      author: { isBot: false, isMe: false, userId: 'user-1', userName: 'Ada' },
      id: 'message-1',
      metadata: { dateSent: new Date('2026-08-30T00:00:00Z') },
      text: 'hello',
      threadId: 'test:thread-1',
    } as unknown as Message
    const adapter = {
      channelIdFromThreadId: () => 'test:channel-1',
      disconnect: vi.fn(() => Promise.resolve()),
      handleWebhook: vi.fn(
        (_request: Request, webhookOptions?: { waitUntil?: (task: Promise<unknown>) => void }) => {
          if (!chat) throw new Error('Chat SDK was not initialized')
          void chat.processMessage(adapter, message.threadId, message, webhookOptions)
          return Promise.resolve(new Response('accepted', { status: 202 }))
        },
      ),
      initialize: vi.fn((instance: Chat) => {
        chat = instance
        return Promise.resolve()
      }),
      name: 'test',
    } as unknown as Adapter
    const runtime = await createChatSdkRuntime({
      adapter,
      integrationAppId: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      logger,
      resolveIdentity: () =>
        Promise.resolve({
          actorDisplayName: 'Ada',
          actorRef: 'user-1',
          conversationKind: 'thread',
          eventType: 'message',
          externalAccountRef: 'account-1',
          externalTenantId: 'tenant-1',
          providerEventId: 'provider-event-1',
        }),
      signal: new AbortController().signal,
      state,
      userName: 'omnara-test',
    })

    const response = await runtime.handleWebhook(new Request('https://example.test/webhook'), {
      reserveWorkBytes: noopWorkReservation,
      resolveInteraction: unexpectedInboundContext.resolveInteraction,
      submitInbound: () => Promise.reject(new Error('core is unavailable')),
      waitUntil: (task) => tracked.push(task),
    })
    await Promise.all(tracked)

    expect(response.status).toBe(202)
    expect(logger.error).toHaveBeenCalledWith('submit Chat SDK inbound event', {
      error: 'core is unavailable',
      integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      provider_event_id: 'provider-event-1',
    })
    await runtime.close()
  })

  it('parses a versioned channel message and preserves opaque destination metadata', () => {
    const delivery = testDelivery({
      delivery_kind: 'message',
      payload: {
        context: { agent_id: 'agt_aaaaaaaaaaaaaaaaaaaaaaaaaa', provider_call_id: 'call-1' },
        destination: {
          channel_id: 'itgt_aaaaaaaaaaaaaaaaaaaaaaaaaa',
          provider_metadata: { guild: 'guild-1' },
          provider_ref: 'thread-1',
          provider_ref_kind: 'thread',
        },
        message: { text: 'hello' },
      },
      payload_version: 'channel-message.v1',
    })

    expect(parseChannelMessageDelivery(delivery)).toEqual(delivery.payload)
  })

  it('rejects channel message text above the gateway byte limit', () => {
    const delivery = testDelivery({
      delivery_kind: 'message',
      payload: {
        context: { agent_id: 'agt_aaaaaaaaaaaaaaaaaaaaaaaaaa', provider_call_id: 'call-1' },
        destination: {
          channel_id: 'itgt_aaaaaaaaaaaaaaaaaaaaaaaaaa',
          provider_metadata: {},
          provider_ref: 'thread-1',
          provider_ref_kind: 'thread',
        },
        message: { text: '界'.repeat(32 * 1024 + 1) },
      },
      payload_version: 'channel-message.v1',
    })

    expect(() => parseChannelMessageDelivery(delivery)).toThrow('payload is malformed')
  })

  it('rejects a payload that tries to redirect a delivery to another target', () => {
    const delivery = testDelivery({
      delivery_kind: 'message',
      payload: {
        context: { agent_id: 'agt_aaaaaaaaaaaaaaaaaaaaaaaaaa', provider_call_id: 'call-1' },
        destination: {
          channel_id: 'itgt_bbbbbbbbbbbbbbbbbbbbbbbbbb',
          provider_metadata: {},
          provider_ref: 'thread-2',
          provider_ref_kind: 'thread',
        },
        message: { text: 'hello' },
      },
      payload_version: 'channel-message.v1',
    })

    expect(() => parseChannelMessageDelivery(delivery)).toThrow('destination is malformed')
  })

  it('parses an interaction prompt without imposing provider rendering behavior', () => {
    const delivery = testDelivery({
      delivery_kind: 'interaction',
      payload: {
        context: { agent_id: 'agt_aaaaaaaaaaaaaaaaaaaaaaaaaa', provider_call_id: 'call-2' },
        destination: {
          channel_id: 'itgt_aaaaaaaaaaaaaaaaaaaaaaaaaa',
          provider_metadata: {},
          provider_ref: 'thread-1',
          provider_ref_kind: 'thread',
        },
        interaction: {
          form: {
            questions: [{ options: [{ label: 'Allow' }, { label: 'Deny' }], prompt: 'Proceed?' }],
            title: 'Permission',
          },
          id: 'aint_aaaaaaaaaaaaaaaaaaaaaaaaaa',
          kind: 'permission',
        },
      },
      payload_version: 'channel-interaction.v1',
    })

    expect(parseChannelInteractionPromptDelivery(delivery)).toEqual(delivery.payload)
    expect(chatSdkDeliveryHandlerKey('interaction', 'channel-interaction.v1')).toBe(
      'interaction\0channel-interaction.v1',
    )
  })

  it('rejects malformed interaction forms before provider code sees them', () => {
    const delivery = testDelivery({
      delivery_kind: 'interaction',
      payload: {
        context: { agent_id: 'agt_aaaaaaaaaaaaaaaaaaaaaaaaaa', provider_call_id: 'call-2' },
        destination: {
          channel_id: 'itgt_aaaaaaaaaaaaaaaaaaaaaaaaaa',
          provider_metadata: {},
          provider_ref: 'thread-1',
          provider_ref_kind: 'thread',
        },
        interaction: {
          form: { questions: [], title: 'Permission' },
          id: 'aint_aaaaaaaaaaaaaaaaaaaaaaaaaa',
          kind: 'permission',
        },
      },
      payload_version: 'channel-interaction.v1',
    })

    expect(() => parseChannelInteractionPromptDelivery(delivery)).toThrow('payload is malformed')
  })

  it('normalizes rate limits as safe retries at the adapter boundary', () => {
    expect(normalizeProviderDeliveryError(new RateLimitError('slow down', 2_500))).toMatchObject({
      outcomeUnknown: false,
      retryAfterMs: 2_500,
      retryable: true,
    })
  })

  it('normalizes definite adapter rejections as permanent failures', () => {
    expect(
      normalizeProviderDeliveryError(new ChatError('missing permission', 'PERMISSION_DENIED')),
    ).toMatchObject({ outcomeUnknown: false, retryable: false })
  })

  it('does not retry an unclassified error after a provider send starts', () => {
    expect(normalizeProviderDeliveryError(new TypeError('connection reset'))).toMatchObject({
      outcomeUnknown: true,
      retryable: false,
    })
  })

  it('rejects remote attachments without a declared size before downloading', async () => {
    const fetchData = vi.fn(() => Promise.resolve(new Uint8Array([1, 2, 3])))
    const message = {
      attachments: [{ fetchData, mimeType: 'image/png', name: 'image.png' }],
      text: '',
    } as unknown as Message

    await expect(messageContentBlocks(message, 10, 10)).rejects.toThrow('declare its size')
    expect(fetchData).not.toHaveBeenCalled()
  })

  it('bounds multibyte inbound text before retaining or submitting it', async () => {
    const message = {
      attachments: [],
      text: '界'.repeat(Math.floor((1024 * 1024) / 3) + 1),
    } as unknown as Message
    const reserveWorkBytes = vi.fn(noopWorkReservation)

    await expect(messageContentBlocks(message, 10, 20, { reserveWorkBytes })).rejects.toThrow(
      'text exceeds its byte limit',
    )
    expect(reserveWorkBytes).not.toHaveBeenCalled()
  })

  it('reserves serialization headroom for accepted inbound text', async () => {
    const reserveWorkBytes = vi.fn(noopWorkReservation)
    const message = { attachments: [], text: '界' } as unknown as Message

    await expect(messageContentBlocks(message, 10, 20, { reserveWorkBytes })).resolves.toEqual([
      { text: '界', type: 'text' },
    ])
    expect(reserveWorkBytes).toHaveBeenCalledWith(9)
  })

  it('rejects an oversized Blob before allocating its bytes', async () => {
    const blob = new Blob([new Uint8Array(11)])
    const arrayBuffer = vi.spyOn(blob, 'arrayBuffer')
    const message = {
      attachments: [{ data: blob, mimeType: 'image/png', name: 'image.png' }],
      text: '',
    } as unknown as Message

    await expect(messageContentBlocks(message, 10, 20)).rejects.toThrow('per-item byte limit')
    expect(arrayBuffer).not.toHaveBeenCalled()
  })

  it('bounds content blocks and reports deterministically omitted attachments', async () => {
    const message = {
      attachments: Array.from({ length: 101 }, (_, index) => ({
        mimeType: 'application/x-unsupported',
        name: `image-${index}.png`,
      })),
      text: '',
    } as unknown as Message

    const blocks = await messageContentBlocks(message, 10, 200)

    expect(blocks).toHaveLength(100)
    expect(blocks.at(-1)).toEqual({
      text: '[2 additional channel attachments omitted]',
      type: 'text',
    })
  })

  it('bounds media items before downloading provider attachments', async () => {
    const fetchData = vi.fn(() => Promise.resolve(Buffer.from([1])))
    const message = {
      attachments: Array.from({ length: 25 }, (_, index) => ({
        fetchData,
        mimeType: 'image/png',
        name: `image-${index}.png`,
        size: 1,
      })),
      text: '',
    } as unknown as Message

    const blocks = await messageContentBlocks(message, 10, 200, {
      fetchAttachmentData: (attachment) => {
        if (!attachment.fetchData) throw new Error('missing test attachment loader')
        return attachment.fetchData()
      },
    })

    expect(blocks).toHaveLength(21)
    expect(fetchData).toHaveBeenCalledTimes(20)
    expect(blocks.at(-1)).toEqual({
      text: '[5 additional channel attachments omitted]',
      type: 'text',
    })
  })

  it('normalizes standard ArrayBuffer attachment data from Chat SDK adapters', async () => {
    const message = {
      attachments: [
        {
          fetchData: () => Promise.resolve(Uint8Array.from([1, 2, 3]).buffer),
          mimeType: 'image/png',
          name: 'image.png',
          size: 3,
        },
      ],
      text: '',
    } as unknown as Message

    await expect(
      messageContentBlocks(message, 10, 20, {
        fetchAttachmentData: (attachment) => {
          if (!attachment.fetchData) throw new Error('missing test attachment loader')
          return attachment.fetchData()
        },
      }),
    ).resolves.toEqual([
      { data: 'AQID', filename: 'image.png', media_type: 'image/png', type: 'media' },
    ])
  })

  it('preflights Buffer bytes and bounds provider filenames to the core schema', async () => {
    const oversized = {
      attachments: [{ data: Buffer.alloc(11), mimeType: 'image/png', name: 'oversized.png' }],
      text: '',
    } as unknown as Message
    await expect(messageContentBlocks(oversized, 10, 20)).rejects.toThrow('per-item byte limit')

    const longName = `${'🖼️'.repeat(260)}.png`
    const bounded = {
      attachments: [{ data: Buffer.from([1]), mimeType: 'image/png', name: longName }],
      text: '',
    } as unknown as Message
    const [block] = await messageContentBlocks(bounded, 10, 20)

    expect(block?.type).toBe('media')
    if (block?.type !== 'media') throw new Error('expected media block')
    expect(Buffer.byteLength(block.filename ?? '', 'utf8')).toBeLessThanOrEqual(255)
    expect(longName.startsWith(block.filename ?? '')).toBe(true)
  })

  it('aborts a hung provider media loader with the inbound signal', async () => {
    const controller = new AbortController()
    let loaderSignal: AbortSignal | undefined
    const message = {
      attachments: [
        {
          fetchData: () => Promise.resolve(Buffer.from([1])),
          mimeType: 'image/png',
          name: 'image.png',
          size: 1,
        },
      ],
      text: '',
    } as unknown as Message
    const loading = messageContentBlocks(message, 10, 20, {
      fetchAttachmentData: (_attachment, context) => {
        loaderSignal = context.signal
        return new Promise<Buffer>(() => undefined)
      },
      fetchTimeoutMs: 1_000,
      signal: controller.signal,
    })
    await vi.waitFor(() => {
      expect(loaderSignal).toBeDefined()
    })

    controller.abort(new Error('webhook deadline reached'))
    await expect(loading).rejects.toThrow('webhook deadline reached')
    expect(loaderSignal?.aborted).toBe(true)
  })

  it('stops a provider media stream as soon as its byte bound is crossed', async () => {
    let canceled = false
    const fetchMock = vi.fn(() =>
      Promise.resolve(
        new Response(
          new ReadableStream({
            cancel: () => {
              canceled = true
            },
            start(controller) {
              controller.enqueue(Uint8Array.from([1, 2, 3]))
              controller.enqueue(Uint8Array.from([4, 5, 6]))
            },
          }),
        ),
      ),
    )
    vi.stubGlobal('fetch', fetchMock)
    try {
      await expect(
        fetchBoundedMedia(
          'https://provider.example/media',
          {},
          {
            maxBytes: 5,
            signal: new AbortController().signal,
          },
        ),
      ).rejects.toThrow('bounded media byte limit')
      await vi.waitFor(() => {
        expect(canceled).toBe(true)
      })
    } finally {
      vi.unstubAllGlobals()
    }
  })
})

function testDelivery(
  input: Pick<ChannelConnectorDelivery, 'delivery_kind' | 'payload' | 'payload_version'>,
): ChannelConnectorDelivery {
  return {
    attempt_count: 1,
    available_at: '2026-08-30T00:00:00Z',
    claim_generation: 1,
    connector_key: 'chat_sdk_v1',
    created_at: '2026-08-30T00:00:00Z',
    id: 'idel_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    integration_install_id: 'iin_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    integration_target_binding_id: 'ibnd_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    integration_target_id: 'itgt_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    last_error: {},
    provider: 'discord',
    state: 'claimed',
    updated_at: '2026-08-30T00:00:00Z',
    ...input,
  }
}

const noopLogger = {
  debug: () => undefined,
  error: () => undefined,
  info: () => undefined,
  warn: () => undefined,
}

const unexpectedInboundContext = {
  resolveInteraction: () => Promise.reject(new Error('unexpected interaction')),
  submitInbound: () => Promise.reject(new Error('unexpected inbound event')),
}

const testInboundEvent: ChannelInboundEventRequest = {
  actor: { display_name: 'Ada', metadata: {}, ref: 'user-1' },
  content_blocks: [{ text: 'hello', type: 'text' }],
  conversation: {
    direct: false,
    kind: 'thread',
    mentioned: true,
    metadata: {},
    ref: 'thread-1',
  },
  event_type: 'message',
  external_account_ref: 'account-1',
  external_tenant_id: 'tenant-1',
  metadata: {},
  occurred_at: '2026-08-30T00:00:00Z',
  provider_event_id: 'provider-event-1',
  version: 'v1',
}

const testInstallation = {
  app_configuration_revision: 1,
  install: {
    configuration_revision: 1,
    id: 'iin_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    provider_account_ref: 'account-1',
    provider_agent_display_name: 'Omnara',
    provider_config: {},
    provider_identity: {},
    provider_metadata: {},
    provider_tenant_id: 'tenant-1',
    updated_at: '2026-08-30T00:00:00Z',
  },
  integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
} satisfies ChannelConnectorInstallationConfiguration

function noopWorkReservation() {
  return { release: () => undefined, resize: () => undefined }
}

function deferred(): { promise: Promise<void>; resolve(): void } {
  let resolve!: () => void
  const promise = new Promise<void>((settle) => {
    resolve = settle
  })
  return { promise, resolve }
}
