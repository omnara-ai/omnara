import type { IncomingMessage, ServerResponse } from 'node:http'

import {
  type ChannelConnectorEventReceipt,
  type ChannelConnectorInstallationConfiguration,
  schemas,
} from '@omnara/sdk'
import { vi } from 'vitest'

import type { SlackBehaviorContext } from './behavior'
import { credentials, json, slackServer } from './test-support'

export const plainKey = 'slack:message:T1:C1:100.000002'
export const filesKey = 'slack:message-files:T1:C1:100.000002'
export const event = {
  type: 'app_mention',
  user: 'U1',
  channel: 'C1',
  channel_type: 'channel',
  ts: '100.000002',
  text: '<@UBOT> use <#C2|fallback> &amp; help',
}
export const id = 'aaaaaaaaaaaaaaaaaaaaaaaaae'
export const result = {
  agent_id: `agt_${id}`,
  channel_id: `itgt_${id}`,
  agent_input_id: `ain_${id}`,
  created_agent: true,
  created_input: true,
  content_blocks: [],
}
export const installation: ChannelConnectorInstallationConfiguration = {
  integration_app_id: `iapp_${id}`,
  app_configuration_revision: 1,
  install: {
    id: `iin_${id}`,
    project_id: 'proj_aaaaaaaaaaaaaaaaaaaaaaaaae',
    provider_account_ref: 'A1',
    provider_tenant_id: 'T1',
    display_name: 'Omnara',
    provider_config: {},
    provider_identity: { bot_user_id: credentials.botUserId },
    metadata: {},
    configuration_revision: 1,
    updated_at: '2026-09-14T00:00:00Z',
  },
  credential: {
    kind: 'slack_app_credentials',
    payload: {
      access_token: credentials.botToken,
      signing_secret: credentials.signingSecret,
    },
  },
}
export function receipt(
  extra: ChannelConnectorEventReceipt['payload'] = {},
): ChannelConnectorEventReceipt {
  return {
    receipt_id: `irec_${id}`,
    integration_app_id: installation.integration_app_id,
    integration_install_id: installation.install.id,
    event_id: 'Ev1',
    state: 'processing',
    lease_token: '01994550-1234-7123-8123-123456789abc',
    lease_generation: 9,
    lease_expires_at: new Date(Date.now() + 10_000).toISOString(),
    attempt_count: 3,
    last_error: {},
    payload: {
      type: 'event_callback',
      event_id: 'Ev1',
      team_id: 'T1',
      api_app_id: 'A1',
      authorizations: [{ team_id: 'T1', user_id: 'UBOT', is_bot: true }],
      event: { ...event, ...extra },
    },
  }
}
export async function fixture(
  provider?: (request: IncomingMessage, response: ServerResponse) => boolean,
) {
  const calls: string[] = []
  const apiUrl = await slackServer((request, response) => {
    calls.push(request.url ?? '')
    if (provider?.(request, response)) return
    switch (request.url) {
      case '/conversations.history':
      case '/conversations.replies':
        json(response, {
          ok: true,
          messages: [{ ts: '99.999999', user: 'U2', text: 'previous <@UBOT>' }],
        })
        break
      case '/reactions.add':
        json(response, { ok: true })
        break
      case '/users.info':
        json(response, { ok: true, user: { profile: { display_name: 'Alice' } } })
        break
      case '/conversations.info':
        json(response, { ok: true, channel: { name: 'general' } })
        break
      default:
        json(response, { ok: false, error: 'file_not_found' })
    }
  })
  const work = { resize: vi.fn<(bytes: number) => void>(), release: vi.fn<() => void>() }
  const context = {
    apiUrl,
    signal: new AbortController().signal,
    deadlineMs: Date.now() + 10_000,
    getInstallation: vi
      .fn<SlackBehaviorContext['getInstallation']>()
      .mockResolvedValue(installation),
    listRoutes: vi
      .fn<SlackBehaviorContext['listRoutes']>()
      .mockResolvedValue([{ route_id: `iroute_${id}`, grants: { read: false, send: true } }]),
    publishDefinition: vi
      .fn<SlackBehaviorContext['publishDefinition']>()
      .mockImplementation((_receipt, definition) => {
        schemas.zPublishChannelConnectorDefinitionRequest.parse(definition)
        return Promise.resolve({ ...definition, id: `cdef_${id}` })
      }),
    lookupRecipients: vi.fn<SlackBehaviorContext['lookupRecipients']>().mockResolvedValue({
      has_receive_binding_history: false,
      workflow_started: false,
      recipients: [],
      next_cursor: null,
    }),
    deliverInput: vi
      .fn<SlackBehaviorContext['deliverInput']>()
      .mockResolvedValue({ ...result, created_agent: false }),
    lookupWorkflow: vi
      .fn<SlackBehaviorContext['lookupWorkflow']>()
      .mockResolvedValue({ exists: false, input_keys: [] }),
    deliverWorkflow: vi
      .fn<SlackBehaviorContext['deliverWorkflow']>()
      .mockImplementation((queued, request) => {
        // Same generated wire as the real CoreClient, including only receipt-derived proof.
        schemas.zDeliverChannelConnectorWorkflowRequest.parse({
          ...request,
          receipt: {
            receipt_id: queued.receipt_id,
            lease_token: queued.lease_token,
            lease_generation: queued.lease_generation,
          },
        })
        return Promise.resolve({
          ...result,
          created_input: request.input_precondition !== undefined,
        })
      }),
    reserveWorkBytes: vi.fn<SlackBehaviorContext['reserveWorkBytes']>().mockReturnValue(work),
  } satisfies SlackBehaviorContext
  return { context, calls, work }
}
