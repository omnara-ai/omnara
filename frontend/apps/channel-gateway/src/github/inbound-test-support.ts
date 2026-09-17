import { createHmac, randomUUID } from 'node:crypto'

import { type ChannelConnectorEventReceipt, type JsonBody, schemas } from '@omnara/sdk'
import { vi } from 'vitest'

import type { ProviderFactoryContext, ProviderWebhookContext } from '../types'
import { WorkByteBudget } from '../work-budget'
import { githubEvent, projectGitHubEvent } from './events'
import type { GitHubFactoryOptions } from './factory'
import { configuration, githubFixture, json, newCommit, oldCommit, pr } from './test-support'

export const suffix = 'aaaaaaaaaaaaaaaaaaaaaaaaaa'
export const app = {
  app: {
    id: `iapp_${suffix}`,
    provider: 'github',
    connector_key: 'omnara',
    provider_app_ref: '42',
    display_name: 'Fixture',
    provider_config: {},
    configuration_revision: 1,
    updated_at: '2026-09-15T00:00:00Z',
  },
  credential: {
    kind: 'integration_credentials',
    payload: {
      private_key: configuration.privateKey,
      webhook_secret: configuration.webhookSecret,
    },
  },
}
export const installation = {
  integration_app_id: app.app.id,
  app_configuration_revision: 1,
  install: {
    id: `iin_${suffix}`,
    project_id: `proj_${suffix}`,
    provider_tenant_id: '123',
    provider_account_ref: '456',
    display_name: 'Fixture',
    metadata: {},
    provider_identity: {
      repository_owner: 'example',
      repository_name: 'project',
      repository_node_id: 'R_selected',
    },
    configuration_revision: 1,
    updated_at: '2026-09-15T00:00:00Z',
  },
}
export const author = { id: 17, node_id: 'U_human', login: 'human', type: 'User' }
export const webhook = {
  action: 'opened',
  installation: { id: 123 },
  repository: { id: 456, node_id: 'R_selected', name: 'project', owner: { login: 'example' } },
  pull_request: {
    id: 22,
    node_id: 'PR_selected',
    number: 7,
    title: 'Review',
    body: 'Original description',
    state: 'open',
    draft: true,
    updated_at: '2026-09-15T12:00:00Z',
    head: { sha: newCommit },
    base: { sha: oldCommit },
  },
  sender: author,
}
export const nativeComment = {
  id: 33,
  node_id: 'PRRC_1',
  user: author,
  body: 'Original signed finding',
  created_at: '2026-09-15T12:00:00Z',
  updated_at: '2026-09-15T12:00:00Z',
  pull_request_review_id: 44,
  path: 'main.go',
  line: 12,
  side: 'RIGHT',
  commit_id: oldCommit,
}
export function event(body: JsonBody = webhook, type = 'pull_request', deliveryID = randomUUID()) {
  return githubEvent.parse(projectGitHubEvent(JSON.stringify(body), type, deliveryID))
}
export function receipt(payload = event()): ChannelConnectorEventReceipt {
  return {
    receipt_id: `irec_${suffix}`,
    integration_app_id: app.app.id,
    integration_install_id: installation.install.id,
    event_id: payload.delivery_id,
    state: 'processing',
    lease_token: randomUUID(),
    lease_generation: 1,
    lease_expires_at: new Date(Date.now() + 30_000).toISOString(),
    attempt_count: 1,
    last_error: {},
    payload,
  }
}
export function signedRequest(
  body = JSON.stringify(webhook),
  type = 'pull_request',
  deliveryID = randomUUID(),
) {
  return new Request('http://localhost/webhook', {
    method: 'POST',
    body,
    headers: {
      'content-type': 'application/json',
      'x-github-event': type,
      'x-github-delivery': deliveryID,
      'x-hub-signature-256': `sha256=${createHmac('sha256', configuration.webhookSecret).update(body).digest('hex')}`,
    },
  })
}
export function coreFixture() {
  type Core = GitHubFactoryOptions['core']
  return {
    getAppConfiguration: vi.fn<Core['getAppConfiguration']>().mockResolvedValue(app),
    getInstallationConfiguration: vi
      .fn<Core['getInstallationConfiguration']>()
      .mockResolvedValue(installation),
    resolveInstallationConfiguration: vi
      .fn<Core['resolveInstallationConfiguration']>()
      .mockResolvedValue(installation),
    listRoutes: vi
      .fn<Core['listRoutes']>()
      .mockResolvedValue([
        { id: `iroute_${suffix}`, behavior_key: 'github_pr', configuration: {} },
      ]),
    publishDefinition: vi.fn<Core['publishDefinition']>().mockImplementation((_scope, body) =>
      Promise.resolve({
        ...body,
        id: `cdef_${body.kind === 'GITHUB_PR' ? suffix : 'bbbbbbbbbbbbbbbbbbbbbbbbbb'}`,
      }),
    ),
    lookupWorkflow: vi
      .fn<Core['lookupWorkflow']>()
      .mockResolvedValue({ exists: false, input_keys: [] }),
    deliverWorkflow: vi.fn<Core['deliverWorkflow']>().mockImplementation((queued, body) => {
      schemas.zDeliverChannelConnectorWorkflowRequest.parse({
        ...body,
        receipt: {
          receipt_id: queued.receipt_id,
          lease_token: queued.lease_token,
          lease_generation: queued.lease_generation,
        },
      })
      return Promise.resolve({
        agent_id: `agt_${suffix}`,
        channel_id: `itgt_${suffix}`,
        agent_input_id: `ain_${suffix}`,
        created_agent: true,
        created_input: true,
        content_blocks: [],
      })
    }),
    listInstallationControlScopes: vi
      .fn<Core['listInstallationControlScopes']>()
      .mockResolvedValue({
        app_configuration_revision: 1,
        installations: [],
        through_installation_id: null,
        next_after_installation_id: null,
      }),
    submitControlEvent: vi.fn<Core['submitControlEvent']>().mockImplementation((_app, _body) =>
      Promise.resolve({
        receipt_id: `icrc_${suffix}`,
        state: 'pending',
        last_installation_id: null,
        end_installation_id: null,
      }),
    ),
    setInstallationProviderState: vi
      .fn<Core['setInstallationProviderState']>()
      .mockImplementation((_app, _install, body) =>
        Promise.resolve({
          state: body.state,
          configuration_revision: body.expected_configuration_revision + 1,
        }),
      ),
  }
}
export function contexts() {
  const budget = new WorkByteBudget(256 * 1024 * 1024)
  const controller = new AbortController()
  const factory: ProviderFactoryContext = {
    configuration: app,
    resolveInstallation: vi.fn().mockResolvedValue(installation),
    signal: controller.signal,
    logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
    reserveWorkBytes: budget.reserve,
  }
  const submitInbound = vi
    .fn<ProviderWebhookContext['submitInbound']>()
    .mockResolvedValue({ receipt_id: `irec_${suffix}`, state: 'pending' })
  const intake: ProviderWebhookContext = {
    reserveWorkBytes: budget.reserve,
    submitInbound,
    resolveInteraction: vi.fn(),
  }
  return { budget, controller, factory, intake, submitInbound }
}
export async function nativeFixture(
  options: { pending?: boolean; missing?: boolean; foreign?: boolean } = {},
) {
  return githubFixture((request, response) => {
    if (request.query.includes('query GitHubViewer'))
      json(response, { data: { viewer: { id: 'U_bot', login: 'example[bot]' } } })
    else if (request.query.includes('query GitHubInboundComment'))
      json(response, {
        data: {
          node: options.missing
            ? null
            : {
                id: request.variables.comment ?? null,
                state: options.pending ? 'PENDING' : 'SUBMITTED',
                replyTo: null,
                pullRequestReview: {
                  id: 'PRR_1',
                  state: options.pending ? 'PENDING' : 'COMMENTED',
                },
                pullRequest: options.foreign ? { ...pr, number: 99 } : pr,
              },
        },
      })
    else if (request.query.includes('query GitHubInboundThreads'))
      json(response, {
        data: {
          node: {
            id: 'R_selected',
            pullRequest: {
              ...pr,
              reviewThreads: {
                nodes: [
                  { id: 'PRRT_1', comments: { nodes: [{ id: 'PRRC_1', state: 'SUBMITTED' }] } },
                ],
                pageInfo: { hasNextPage: false, endCursor: null },
              },
            },
          },
        },
      })
    else json(response, {}, 404)
  })
}
