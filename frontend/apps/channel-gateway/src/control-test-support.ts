import type { ChannelConnectorControlReceipt } from '@omnara/sdk'

export const controlCapability = { connector_key: 'omnara', provider: 'github' }
export const nextInstallationId = 'iin_bbbbbbbbbbbbbbbbbbbbbbbbbb'

export function controlReceipt(): ChannelConnectorControlReceipt {
  return {
    receipt_id: 'icrc_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    provider_tenant_id: '123',
    event_id: 'provider-delivery',
    payload: { type: 'verified-control', lease_token: 'payload-is-not-authority' },
    state: 'processing',
    last_installation_id: null,
    end_installation_id: 'iin_cccccccccccccccccccccccccc',
    lease_token: '01994550-1234-7123-8123-123456789abc',
    lease_generation: 3,
    lease_expires_at: new Date(Date.now() + 1_000).toISOString(),
    attempts_since_progress: 1,
    last_error: {},
    created_at: '2026-09-15T12:00:00Z',
  }
}
