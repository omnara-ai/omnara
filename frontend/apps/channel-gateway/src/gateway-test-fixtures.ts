import type { ChannelConnectorRuntimeUnit } from '@omnara/sdk'
import { type Adapter, Message, type MessageData, type StateAdapter } from 'chat'
import { vi } from 'vitest'

import type { RuntimeHandle } from './app-registry'
import type { GatewayAppConfiguration } from './types'

export function unexpectedTestCall(): never {
  throw new Error('unexpected test dependency call')
}

export function testAppConfiguration(
  overrides: Partial<GatewayAppConfiguration['app']> = {},
): GatewayAppConfiguration {
  return {
    app: {
      configuration_revision: 1,
      connector_key: 'chat_sdk_v1',
      display_name: 'Test',
      id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      provider: 'discord',
      provider_app_ref: 'app-1',
      provider_config: {},
      provider_metadata: {},
      updated_at: '2026-08-30T00:00:00Z',
      ...overrides,
    },
  }
}

export function testRuntimeHandle(overrides: Partial<RuntimeHandle> = {}): RuntimeHandle {
  return {
    configuration: testAppConfiguration(),
    getInstallation: vi.fn<RuntimeHandle['getInstallation']>(unexpectedTestCall),
    handleWebhook: vi.fn<RuntimeHandle['handleWebhook']>(unexpectedTestCall),
    release: vi.fn<RuntimeHandle['release']>(() => Promise.resolve()),
    resolveInstallation: vi.fn<RuntimeHandle['resolveInstallation']>(unexpectedTestCall),
    runUnit: vi.fn<RuntimeHandle['runUnit']>(unexpectedTestCall),
    runtime: {
      close: () => Promise.resolve(),
      handleWebhook: unexpectedTestCall,
      send: unexpectedTestCall,
    },
    ...overrides,
  }
}

export function testRuntimeUnit(
  overrides: Partial<ChannelConnectorRuntimeUnit> = {},
): ChannelConnectorRuntimeUnit {
  return {
    checkpoint: {},
    checkpoint_revision: 0,
    checkpoint_version: 1,
    configuration: {},
    created_at: '2026-08-30T00:00:00Z',
    desired_state: 'running',
    id: 'irun_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    last_error: {},
    lease_app_configuration_revision: 1,
    lease_generation: 1,
    lease_spec_revision: 1,
    lease_token: '00000000-0000-7000-8000-000000000001',
    runtime_kind: 'provider_gateway',
    spec_revision: 1,
    status: 'running',
    unit_key: 'shard-0',
    updated_at: '2026-08-30T00:00:00Z',
    ...overrides,
  }
}

export function testMessage(overrides: Partial<MessageData> = {}): Message {
  return new Message({
    attachments: [],
    author: { fullName: 'Ada', isBot: false, isMe: false, userId: 'user-1', userName: 'Ada' },
    formatted: { children: [], type: 'root' },
    id: 'message-1',
    metadata: { dateSent: new Date('2026-08-30T00:00:00Z'), edited: false },
    raw: {},
    text: '',
    threadId: 'test:thread-1',
    ...overrides,
  })
}

export function testAdapter(overrides: Partial<Adapter> = {}): Adapter {
  return {
    addReaction: unexpectedTestCall,
    channelIdFromThreadId: () => 'test:channel-1',
    decodeThreadId: unexpectedTestCall,
    deleteMessage: unexpectedTestCall,
    editMessage: unexpectedTestCall,
    encodeThreadId: unexpectedTestCall,
    fetchMessages: unexpectedTestCall,
    fetchThread: unexpectedTestCall,
    handleWebhook: unexpectedTestCall,
    initialize: () => Promise.resolve(),
    name: 'test',
    parseMessage: unexpectedTestCall,
    postMessage: unexpectedTestCall,
    removeReaction: unexpectedTestCall,
    renderFormatted: unexpectedTestCall,
    startTyping: unexpectedTestCall,
    userName: 'omnara-test',
    ...overrides,
  }
}

export function testStateAdapter(overrides: Partial<StateAdapter> = {}): StateAdapter {
  return {
    acquireLock: unexpectedTestCall,
    appendToList: unexpectedTestCall,
    connect: () => Promise.resolve(),
    delete: unexpectedTestCall,
    dequeue: unexpectedTestCall,
    disconnect: () => Promise.resolve(),
    enqueue: unexpectedTestCall,
    extendLock: unexpectedTestCall,
    forceReleaseLock: unexpectedTestCall,
    get: unexpectedTestCall,
    getList: unexpectedTestCall,
    isSubscribed: unexpectedTestCall,
    queueDepth: unexpectedTestCall,
    releaseLock: unexpectedTestCall,
    set: unexpectedTestCall,
    setIfNotExists: unexpectedTestCall,
    subscribe: unexpectedTestCall,
    unsubscribe: unexpectedTestCall,
    ...overrides,
  }
}
