import type { ChannelConnectorRuntimeUnit } from '@omnara/sdk'
import { vi } from 'vitest'

import type { RuntimeHandle } from './runtime/registry'
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
      connector_key: 'test_connector',
      display_name: 'Test',
      id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      provider: 'discord',
      provider_app_ref: 'app-1',
      provider_config: {},
      updated_at: '2026-08-30T00:00:00Z',
      ...overrides,
    },
  }
}

export function testRuntimeHandle(overrides: Partial<RuntimeHandle> = {}): RuntimeHandle {
  return {
    configuration: testAppConfiguration(),
    handleWebhook: vi.fn<RuntimeHandle['handleWebhook']>(unexpectedTestCall),
    release: vi.fn<RuntimeHandle['release']>(() => Promise.resolve()),
    runUnit: vi.fn<RuntimeHandle['runUnit']>(unexpectedTestCall),
    runtime: {
      close: () => Promise.resolve(),
      handleWebhook: unexpectedTestCall,
    },
    ...overrides,
  }
}

export function testRuntimeUnit(
  overrides: Partial<ChannelConnectorRuntimeUnit> = {},
): ChannelConnectorRuntimeUnit {
  return {
    checkpoint: {},
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
