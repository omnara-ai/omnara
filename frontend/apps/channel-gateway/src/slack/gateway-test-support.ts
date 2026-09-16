import { vi } from 'vitest'

import { WorkByteBudget } from '../work-budget'
import { createSlackGateway, type SlackGatewayOptions } from './gateway'
import { credentials } from './test-support'

export const suffix = 'aaaaaaaaaaaaaaaaaaaaaaaaae'
export const scope = {
  project_id: `proj_${suffix}`,
  integration_app_id: `iapp_${suffix}`,
  integration_install_id: `iin_${suffix}`,
  agent_id: `agt_${suffix}`,
  channel_id: `itgt_${suffix}`,
}
export const app = {
  app: {
    id: scope.integration_app_id,
    provider: 'slack',
    connector_key: 'omnara',
    provider_app_ref: 'A1',
    display_name: 'Omnara',
    provider_config: {},
    provider_metadata: {},
    configuration_revision: 4,
    updated_at: '2026-09-14T00:00:00Z',
  },
}
export const installation = {
  integration_app_id: scope.integration_app_id,
  app_configuration_revision: 4,
  install: {
    id: scope.integration_install_id,
    project_id: scope.project_id,
    provider_account_ref: 'A1',
    provider_tenant_id: 'T1',
    display_name: 'Omnara',
    provider_config: {},
    provider_identity: { bot_user_id: credentials.botUserId },
    metadata: {},
    configuration_revision: 8,
    updated_at: '2026-09-14T00:00:00Z',
  },
  credential: {
    kind: 'slack_app_credentials',
    payload: { access_token: credentials.botToken },
  },
}
export const delivered = {
  agent_id: scope.agent_id,
  channel_id: scope.channel_id,
  agent_input_id: `ain_${suffix}`,
  created_agent: true,
  created_input: true,
  content_blocks: [],
  canceled_interaction_ids: [],
}
export function setup(apiUrl?: string) {
  const core = {
    getAppConfiguration: vi
      .fn<SlackGatewayOptions['core']['getAppConfiguration']>()
      .mockResolvedValue(app),
    getInstallationConfiguration: vi
      .fn<SlackGatewayOptions['core']['getInstallationConfiguration']>()
      .mockResolvedValue(installation),
    listRoutes: vi
      .fn<SlackGatewayOptions['core']['listRoutes']>()
      .mockResolvedValue([
        { id: `iroute_${suffix}`, behavior_key: 'slack_conversation', configuration: {} },
      ]),
    publishDefinition: vi
      .fn<SlackGatewayOptions['core']['publishDefinition']>()
      .mockImplementation((_receipt, body) => Promise.resolve({ ...body, id: `cdef_${suffix}` })),
    lookupRecipients: vi.fn<SlackGatewayOptions['core']['lookupRecipients']>().mockResolvedValue({
      has_receive_binding_history: false,
      workflow_started: false,
      recipients: [],
      next_cursor: null,
    }),
    deliverInput: vi
      .fn<SlackGatewayOptions['core']['deliverInput']>()
      .mockResolvedValue({ ...delivered, created_agent: false }),
    lookupWorkflow: vi
      .fn<SlackGatewayOptions['core']['lookupWorkflow']>()
      .mockResolvedValue({ exists: false, input_keys: [] }),
    deliverWorkflow: vi
      .fn<SlackGatewayOptions['core']['deliverWorkflow']>()
      .mockResolvedValue(delivered),
  }
  const workBudget = new WorkByteBudget(256 * 1024 * 1024)
  const handlers = createSlackGateway({ core, workBudget, apiUrl })
  return { ...handlers, core, workBudget }
}
