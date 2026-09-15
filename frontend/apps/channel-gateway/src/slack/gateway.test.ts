import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Readable } from 'node:stream'

import {
  ApiError,
  type ChannelConnectorEventReceipt,
  type ChannelOpaqueObject,
  schemas,
} from '@omnara/sdk'
import { describe, expect, it, onTestFinished, vi } from 'vitest'

import { CoreClient } from '../core-client'
import type { GatewayOperation } from '../operations'
import { OperationFiles } from '../operations-files'
import { ReceiptClientError } from '../receipt-http'
import { ReceiptBehaviorError } from '../types'
import { WorkByteBudget } from '../work-budget'
import { createSlackGateway, type SlackGatewayOptions } from './gateway'
import { body, credentials, deferred, json, slackServer } from './test-support'

const suffix = 'aaaaaaaaaaaaaaaaaaaaaaaaae'
const scope = {
  project_id: `proj_${suffix}`,
  integration_app_id: `iapp_${suffix}`,
  integration_install_id: `iin_${suffix}`,
  agent_id: `agt_${suffix}`,
  channel_id: `itgt_${suffix}`,
}
const app = {
  app: {
    id: scope.integration_app_id,
    provider: 'slack',
    connector_key: 'chat_sdk',
    provider_app_ref: 'A1',
    display_name: 'Omnara',
    provider_config: {},
    provider_metadata: {},
    configuration_revision: 4,
    updated_at: '2026-09-14T00:00:00Z',
  },
}
const installation = {
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
    payload: { access_token: credentials.botToken, signing_secret: credentials.signingSecret },
  },
}
const delivered = {
  agent_id: scope.agent_id,
  channel_id: scope.channel_id,
  agent_input_id: `ain_${suffix}`,
  created_agent: true,
  created_input: true,
  content_blocks: [],
  canceled_interaction_ids: [],
}
const destination = {
  implementation_key: 'slack_channel',
  provider_ref: 'C1',
  provider_ref_kind: 'channel',
  provider_metadata: {},
}
const send = { destination, message: { text: 'hello' }, params: {} }
const interaction = {
  destination: {
    ...destination,
    implementation_key: 'slack_thread',
    provider_ref: 'C1:100.000001',
    provider_ref_kind: 'thread',
  },
  interaction_id: `int_${suffix}`,
  agent_id: scope.agent_id,
  channel_id: scope.channel_id,
  kind: 'permission',
  form: {
    title: 'Permission required',
    questions: [{ prompt: 'Allow?', options: [{ label: 'Allow once' }, { label: 'Deny' }] }],
  },
}

function operation(
  kind: GatewayOperation['kind'] = 'send',
  payload: ChannelOpaqueObject = send,
): GatewayOperation {
  return {
    kind,
    scope,
    capability: { connector_key: 'chat_sdk', provider: 'slack' },
    requestId: 'request-1',
    deadlineMs: Date.now() + 10_000,
    artifacts: [],
    payloadJSON: JSON.stringify(payload),
  }
}
function receipt(): ChannelConnectorEventReceipt {
  return {
    receipt_id: `irec_${suffix}`,
    integration_app_id: scope.integration_app_id,
    integration_install_id: scope.integration_install_id,
    event_id: 'Ev1',
    state: 'processing',
    lease_token: '01994550-1234-7123-8123-123456789abc',
    lease_generation: 3,
    lease_expires_at: new Date(Date.now() + 10_000).toISOString(),
    attempt_count: 1,
    last_error: {},
    payload: {
      type: 'event_callback',
      team_id: 'T1',
      api_app_id: 'A1',
      event_id: 'Ev1',
      authorizations: [{ team_id: 'T1', user_id: 'UBOT', is_bot: true }],
      event: {
        type: 'message',
        channel: 'D1',
        channel_type: 'im',
        user: 'U1',
        ts: '100.000001',
        text: 'hello',
      },
    },
  }
}
function setup(apiUrl?: string) {
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
const context = () => ({ deadlineMs: Date.now() + 10_000, signal: new AbortController().signal })

describe('Slack gateway receipt composition', () => {
  it('uses exact queued IDs, supported configured routes and the fixed first-party grants', async () => {
    const requests: string[] = []
    const url = await slackServer((request, response) => {
      requests.push(request.url ?? '')
      if (request.url === '/reactions.add') json(response, { ok: true })
      else json(response, { ok: false, error: 'missing_scope' })
    })
    const { processReceipt, core, workBudget } = setup(url)
    const queued = receipt()
    await processReceipt(queued, context())
    expect(core.getInstallationConfiguration).toHaveBeenCalledWith(
      scope.integration_app_id,
      scope.integration_install_id,
      expect.any(AbortSignal),
    )
    const routeCall = core.listRoutes.mock.calls[0]
    expect(routeCall?.[0]).toBe(queued)
    expect(routeCall?.[1]).toBeInstanceOf(AbortSignal)
    expect(routeCall?.[2]?.resize).toBeTypeOf('function')
    expect(routeCall?.[2]?.release).toBeTypeOf('function')
    expect(core.lookupWorkflow).toHaveBeenCalledWith(
      queued,
      {
        route_id: `iroute_${suffix}`,
        instance_key: 'D1',
        input_keys: ['slack:message:T1:D1:100.000001', 'slack:message-files:T1:D1:100.000001'],
      },
      expect.any(AbortSignal),
    )
    expect(core.publishDefinition.mock.calls[0]?.[1].implementation_key).toBe('slack_channel')
    expect(core.deliverWorkflow.mock.calls[0]?.[0]).toBe(queued)
    expect(core.deliverWorkflow.mock.calls[0]?.[1].grants).toEqual({ read: true, send: true })
    expect(requests).toContain('/reactions.add')
    expect(workBudget.usedBytes).toBe(0)
  })

  it('uses a receive binding before routes in the production composition', async () => {
    const apiUrl = await slackServer((_request, response) => {
      json(response, { ok: false, error: 'missing_scope' })
    })
    const { core, processReceipt, workBudget } = setup(apiUrl)
    core.listRoutes.mockResolvedValue([])
    core.lookupRecipients.mockResolvedValue({
      channel_id: scope.channel_id,
      has_receive_binding_history: true,
      workflow_started: false,
      recipients: [{ agent_id: scope.agent_id, binding_id: `ibnd_${suffix}`, input_keys: [] }],
      next_cursor: null,
    })
    const queued = receipt()
    await processReceipt(queued, context())
    expect(core.lookupRecipients).toHaveBeenCalledWith(
      queued,
      {
        provider_ref: 'D1',
        input_keys: ['slack:message:T1:D1:100.000001', 'slack:message-files:T1:D1:100.000001'],
      },
      expect.any(AbortSignal),
    )
    expect(core.deliverInput).toHaveBeenCalledWith(
      queued,
      expect.objectContaining({
        binding_id: `ibnd_${suffix}`,
        input_key: 'slack:message:T1:D1:100.000001',
      }),
      expect.any(AbortSignal),
    )
    expect(core.listRoutes).not.toHaveBeenCalled()
    expect(core.deliverWorkflow).not.toHaveBeenCalled()
    expect(workBudget.usedBytes).toBe(0)
  })

  it.each([
    { behavior_key: 'unsupported', configuration: {} },
    { behavior_key: 'slack_conversation', configuration: { agent_profile_id: 'spoofed' } },
    { behavior_key: 'slack_conversation', configuration: { handler_version: 1 } },
  ])('rejects the complete route list without partial delivery: %j', async (route) => {
    const providerCalls: string[] = []
    const apiUrl = await slackServer((request, response) => {
      providerCalls.push(request.url ?? '')
      json(response, { ok: false, error: 'missing_scope' })
    })
    const { core, processReceipt, workBudget } = setup(apiUrl)
    // A usable route precedes the invalid one: incremental validation would
    // already have admitted input before discovering the unsupported route.
    core.listRoutes.mockResolvedValue([
      { id: `iroute_${suffix}`, behavior_key: 'slack_conversation', configuration: {} },
      { id: 'iroute_aaaaaaaaaaaaaaaaaaaaaaaabi', ...route },
      {
        id: 'iroute_aaaaaaaaaaaaaaaaaaaaaaaabm',
        behavior_key: 'slack_conversation',
        configuration: {},
      },
    ])
    await expect(processReceipt(receipt(), context())).rejects.toEqual(
      new ReceiptBehaviorError(false),
    )
    expect(core.lookupWorkflow).not.toHaveBeenCalled()
    expect(core.publishDefinition).not.toHaveBeenCalled()
    expect(core.deliverWorkflow).not.toHaveBeenCalled()
    expect(providerCalls).toEqual([])
    expect(workBudget.usedBytes).toBe(0)
  })

  it.each([
    { error: new ReceiptClientError('transport_failed'), retryable: true },
    { error: new ReceiptClientError('http_error', 409), retryable: true },
    { error: new ReceiptClientError('http_error', 503), retryable: true },
    { error: new ReceiptClientError('http_error', 403), retryable: false },
    { error: new ReceiptClientError('invalid_response'), retryable: false },
    { error: new ApiError(404, 'private provider diagnostic'), retryable: false },
  ])(
    'classifies known core failures without retaining diagnostic text: $retryable',
    async ({ error, retryable }) => {
      const { processReceipt, core } = setup()
      core.listRoutes.mockRejectedValue(error)
      await expect(processReceipt(receipt(), context())).rejects.toEqual(
        new ReceiptBehaviorError(retryable),
      )
    },
  )

  it.each([true, false])(
    'holds route memory through strict mapping and releases on success/failure: %s',
    async (supported) => {
      const { processReceipt, core, workBudget } = setup()
      core.lookupWorkflow.mockResolvedValue({
        exists: true,
        agent_state: 'archived',
        input_keys: [],
      })
      core.listRoutes.mockImplementation((_queued, _signal, work) => {
        if (!work) throw new Error('missing route reservation')
        work.resize(1234)
        expect(workBudget.usedBytes).toBe(1234)
        return Promise.resolve([
          {
            id: `iroute_${suffix}`,
            behavior_key: 'slack_conversation',
            configuration: supported ? {} : { unsupported: 'value' },
          },
        ])
      })
      if (supported) await processReceipt(receipt(), context())
      else
        await expect(processReceipt(receipt(), context())).rejects.toMatchObject({
          retryable: false,
        })
      expect(workBudget.usedBytes).toBe(0)
    },
  )

  it.each(['dm', 'thread'])(
    'posts the old notice only for definite admission denial in a %s',
    async (kind) => {
      const posts: unknown[] = []
      const url = await slackServer((request, response) => {
        if (request.url === '/chat.postMessage')
          void body(request).then((bytes) => {
            posts.push(JSON.parse(bytes.toString()))
            // A cosmetic failure must still leave a definite, non-retryable denial.
            json(response, { ok: false, error: 'internal_error' })
          })
        else json(response, { ok: false, error: 'missing_scope' })
      })
      const { processReceipt, core } = setup(url)
      core.deliverWorkflow.mockRejectedValue(
        new ReceiptClientError('http_error', 409, 'managed_work_admission_denied'),
      )
      const queued = receipt()
      if (kind === 'thread')
        queued.payload.event = {
          type: 'app_mention',
          channel: 'C1',
          user: 'U1',
          ts: '100.000002',
          thread_ts: '100.000001',
          text: 'hello',
        }
      await expect(processReceipt(queued, context())).rejects.toEqual(
        new ReceiptBehaviorError(false),
      )
      expect(core.lookupWorkflow).toHaveBeenCalledOnce()
      expect(core.deliverWorkflow).toHaveBeenCalledOnce()
      const expected = {
        channel: kind === 'dm' ? 'D1' : 'C1',
        text: "I couldn't complete this request. Please try again later or contact this bot's owner.",
      }
      expect(posts).toEqual([kind === 'dm' ? expected : { ...expected, thread_ts: '100.000001' }])
    },
  )

  it.each([
    new ReceiptClientError('http_error', 409),
    new ReceiptClientError('http_error', 503),
    new ReceiptClientError('aborted'),
  ])(
    'never posts a failure notice for unclassified conflict, infrastructure failure or abort',
    async (error) => {
      const paths: string[] = []
      const url = await slackServer((request, response) => {
        paths.push(request.url ?? '')
        json(response, { ok: false, error: 'missing_scope' })
      })
      const { processReceipt, core } = setup(url)
      core.deliverWorkflow.mockRejectedValue(error)
      await expect(processReceipt(receipt(), context())).rejects.toBeInstanceOf(
        ReceiptBehaviorError,
      )
      expect(paths).not.toContain('/chat.postMessage')
      expect(paths).not.toContain('/reactions.add')
    },
  )

  it('leaves programming errors unclassified and retries known configuration transport failure', async () => {
    const { processReceipt, core } = setup()
    const bug = new TypeError('unclassified failure')
    core.listRoutes.mockRejectedValue(bug)
    await expect(processReceipt(receipt(), context())).rejects.toBe(bug)
    core.getInstallationConfiguration.mockRejectedValue(new TypeError('fetch failed'))
    await expect(processReceipt(receipt(), context())).rejects.toEqual(
      new ReceiptBehaviorError(true),
    )
  })

  it.each([true, false])(
    'refreshes admission-returned canceled prompts only for new input: %s',
    async (createdInput) => {
      const paths: string[] = []
      const updates: unknown[] = []
      const canceledID = `int_${suffix}`
      const url = await slackServer((request, response) => {
        paths.push(request.url ?? '')
        if (request.url === '/conversations.history')
          json(response, {
            ok: true,
            messages: [
              {
                ts: '99.000001',
                text: 'Permission required',
                blocks: [{ block_id: `omnara_interaction_${canceledID}` }],
              },
            ],
          })
        else if (request.url === '/chat.update')
          void body(request).then((bytes) => {
            updates.push(JSON.parse(bytes.toString()))
            // Cleanup failure must not retry the successful admission.
            json(response, { ok: false, error: 'internal_error' })
          })
        else json(response, { ok: true })
      })
      const { processReceipt, core } = setup(url)
      core.deliverWorkflow.mockResolvedValue({
        ...delivered,
        created_input: createdInput,
        canceled_interaction_ids: [canceledID],
      })
      await processReceipt(receipt(), context())
      expect(core.deliverWorkflow).toHaveBeenCalledOnce()
      expect(updates).toHaveLength(createdInput ? 1 : 0)
      if (createdInput)
        expect(updates[0]).toMatchObject({
          channel: 'D1',
          ts: '99.000001',
          text: 'Permission required\nDismissed because a newer message was sent.',
        })
      else {
        expect(paths).not.toContain('/reactions.add')
        expect(paths).not.toContain('/conversations.history')
        expect(paths).not.toContain('/chat.update')
      }
    },
  )

  it('releases the injected media budget and does not retry accepted input when reaction fails', async () => {
    const paths: string[] = []
    const url = await slackServer((request, response) => {
      paths.push(request.url ?? '')
      if (request.url === '/file') response.end('notes')
      else json(response, { ok: false, error: 'internal_error' })
    })
    const { processReceipt, core, workBudget } = setup(url)
    const queued = receipt()
    queued.payload.event = {
      type: 'message',
      channel: 'D1',
      channel_type: 'im',
      user: 'U1',
      ts: '100.000001',
      text: 'hello',
      files: [{ name: 'notes.txt', url_private: `${url}/file` }],
    }
    await processReceipt(queued, context())
    expect(core.deliverWorkflow).toHaveBeenCalledOnce()
    expect(core.deliverWorkflow.mock.calls[0]?.[1].content_blocks.at(-1)).toMatchObject({
      type: 'media',
      media_type: 'text/plain',
    })
    expect(paths.filter((path) => path === '/reactions.add')).toHaveLength(1)
    expect(workBudget.usedBytes).toBe(0)
  })
})

describe('Slack gateway operation composition', () => {
  it('checks live scope for every operation and returns canonical send/reply facts', async () => {
    const sent: unknown[] = []
    const url = await slackServer((request, response) => {
      expect(request.headers.authorization).toBe(`Bearer ${credentials.botToken}`)
      void body(request).then((bytes) => {
        sent.push(JSON.parse(bytes.toString()))
        json(response, { ok: true, channel: 'C1', ts: '100.000001' })
      })
    })
    const { executeOperation, core } = setup(url)
    const input = operation('send', {
      ...send,
      reply_channel_grants: { receive: true, read: false, send: true },
    })
    const expected = {
      outcome: 'completed',
      payload: {
        publication: 'published',
        message_channel: 'destination',
        message_id: '100.000001',
        reply_channel: {
          implementation_key: 'slack_thread',
          provider_ref: 'C1:100.000001',
          provider_ref_kind: 'thread',
        },
      },
    }
    expect(await executeOperation(input, [], context().signal)).toEqual(expected)
    expect(await executeOperation(input, [], context().signal)).toEqual(expected)
    expect(core.getAppConfiguration).toHaveBeenCalledTimes(2)
    expect(core.getInstallationConfiguration).toHaveBeenCalledTimes(2)
    expect(core.getInstallationConfiguration).toHaveBeenLastCalledWith(
      scope.integration_app_id,
      scope.integration_install_id,
      expect.any(AbortSignal),
    )
    expect(sent).toEqual([
      { channel: 'C1', text: 'hello' },
      { channel: 'C1', text: 'hello' },
    ])
  })

  it.each([
    { implementation_key: 'unregistered', provider_ref_kind: 'channel' },
    { implementation_key: 'slack_thread', provider_ref_kind: 'dm' },
    { implementation_key: 'slack_channel', provider_ref_kind: 'thread' },
  ])('rejects implementation/address mismatch before core or provider I/O: %j', async (patch) => {
    const { executeOperation, core } = setup()
    expect(
      await executeOperation(
        operation('send', { ...send, destination: { ...destination, ...patch } }),
        [],
        context().signal,
      ),
    ).toEqual({ outcome: 'failed' })
    expect(core.getAppConfiguration).not.toHaveBeenCalled()
  })

  it.each([
    '{"destination":{},"destination":{}}',
    '{"message":{"text":"x","te\\u0078t":"y"}}',
    JSON.stringify(send) + '{}',
    JSON.stringify({ ...send, wrapper: {} }),
    JSON.stringify({ ...send, message: { text: 'hello', typo: 'ignored?' } }),
    JSON.stringify({ ...send, destination: { ...destination, url: 'https://evil.invalid' } }),
    JSON.stringify({ ...send, params: null }),
  ])('rejects malformed or unsupported payload without weakening: %s', async (payloadJSON) => {
    const { executeOperation, core } = setup()
    expect(await executeOperation({ ...operation(), payloadJSON }, [], context().signal)).toEqual({
      outcome: 'failed',
    })
    expect(core.getAppConfiguration).not.toHaveBeenCalled()
  })

  it('rejects a mismatched capability before loading credentials', async () => {
    const { executeOperation, core } = setup()
    for (const capability of [
      { connector_key: 'other', provider: 'slack' },
      { connector_key: 'chat_sdk', provider: 'other' },
    ])
      expect(await executeOperation({ ...operation(), capability }, [], context().signal)).toEqual({
        outcome: 'failed',
      })
    expect(core.getAppConfiguration).not.toHaveBeenCalled()
  })

  it.each([
    { field: 'app', value: { ...app, app: { ...app.app, id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaabi' } } },
    { field: 'app', value: { ...app, app: { ...app.app, connector_key: 'other' } } },
    { field: 'app', value: { ...app, app: { ...app.app, provider: 'other' } } },
    {
      field: 'install',
      value: { ...installation, integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaabi' },
    },
    {
      field: 'install',
      value: {
        ...installation,
        install: { ...installation.install, id: 'iin_aaaaaaaaaaaaaaaaaaaaaaaabi' },
      },
    },
    {
      field: 'install',
      value: {
        ...installation,
        install: { ...installation.install, project_id: 'proj_aaaaaaaaaaaaaaaaaaaaaaaabi' },
      },
    },
    {
      field: 'install',
      value: {
        ...installation,
        install: { ...installation.install, provider_account_ref: 'OTHERAPP' },
      },
    },
    { field: 'install', value: { ...installation, app_configuration_revision: 3 } },
  ])('rejects current core scope/identity mismatch: $field', async ({ value }) => {
    const fetch = vi.spyOn(globalThis, 'fetch')
    try {
      const { executeOperation, core } = setup()
      if ('app' in value) {
        expect(schemas.zChannelConnectorAppConfiguration.safeParse(value).success).toBe(true)
        core.getAppConfiguration.mockResolvedValue(value)
      } else {
        expect(schemas.zChannelConnectorInstallationConfiguration.safeParse(value).success).toBe(
          true,
        )
        core.getInstallationConfiguration.mockResolvedValue(value)
      }
      expect(await executeOperation(operation(), [], context().signal)).toEqual({
        outcome: 'failed',
      })
      expect(fetch).not.toHaveBeenCalled()
    } finally {
      fetch.mockRestore()
    }
  })

  it('keeps revoked configuration failures known unsent', async () => {
    const { executeOperation, core } = setup()
    core.getInstallationConfiguration.mockRejectedValue(new ApiError(403, 'private diagnostic'))
    expect(await executeOperation(operation(), [], context().signal)).toEqual({ outcome: 'failed' })
  })

  it('dispatches native history with the public SDK parser and preserves partial file coverage', async () => {
    const url = await slackServer((request, response) => {
      expect(request.url).toBe('/conversations.history')
      json(response, {
        ok: true,
        messages: [{ ts: '100.000001', user: 'U1', text: '*hello*', files: [{ id: 'F1' }] }],
      })
    })
    const { executeOperation } = setup(url)
    const result = await executeOperation(
      operation('read', { destination, limit: 10 }),
      [],
      context().signal,
    )
    expect(result.outcome).toBe('completed')
    if (result.outcome !== 'completed') throw new Error('missing history')
    const history = schemas.zChannelReadOperationResult.parse(result.payload)
    expect(history.coverage).toBe('partial')
    expect(history.messages[0]?.metadata).toEqual({ slack_file_ids: ['F1'] })
    expect(history.messages[0]?.content.text).toContain('hello')
  })

  it('pins interaction IDs to scope before posting and returns presentation only', async () => {
    let post: unknown
    const url = await slackServer((request, response) => {
      void body(request).then((bytes) => {
        post = JSON.parse(bytes.toString())
        json(response, { ok: true, ts: '100.000002' })
      })
    })
    const { executeOperation } = setup(url)
    for (const patch of [
      { agent_id: 'agt_aaaaaaaaaaaaaaaaaaaaaaaabi' },
      { channel_id: 'itgt_aaaaaaaaaaaaaaaaaaaaaaaabi' },
    ])
      expect(
        await executeOperation(
          operation('interaction', { ...interaction, ...patch }),
          [],
          context().signal,
        ),
      ).toEqual({ outcome: 'failed' })
    expect(post).toBeUndefined()
    expect(
      await executeOperation(operation('interaction', interaction), [], context().signal),
    ).toEqual({ outcome: 'completed', payload: { message_id: '100.000002' } })
    expect(JSON.stringify(post)).toContain('integration_target_id')
    expect(JSON.stringify(post)).toContain(scope.channel_id)
  })

  it('stages authorized artifacts and publishes text plus files as one logical message', async () => {
    const paths: string[] = []
    let publication: unknown
    const url = await slackServer((request, response) => {
      paths.push(request.url ?? '')
      if (request.url === '/files.getUploadURLExternal')
        json(response, { ok: true, file_id: 'F1', upload_url: `${url}/upload` })
      else if (request.url === '/upload') void body(request).then(() => response.end())
      else
        void body(request).then((bytes) => {
          publication = JSON.parse(bytes.toString())
          json(response, { ok: true, files: [{ id: 'F1' }] })
        })
    })
    const directory = await mkdtemp(join(tmpdir(), 'omnara-slack-gateway-test-'))
    const files = new OperationFiles(directory, new WorkByteBudget(1024), context().signal)
    onTestFinished(async () => {
      await files.close()
      await rm(directory, { recursive: true, force: true })
    })
    const artifact = await files.save(
      Readable.from(['notes']),
      { id: `art_${suffix}`, filename: 'notes.txt', content_type: 'text/plain' },
      0,
    )
    const { executeOperation } = setup(url)
    const input = operation('send', {
      ...send,
      message: { text: 'hello', artifact_ids: [artifact.id] },
    })
    input.artifacts = [artifact]
    expect((await executeOperation(input, [artifact], context().signal)).outcome).toBe('completed')
    expect(paths).toEqual([
      '/files.getUploadURLExternal',
      '/upload',
      '/files.completeUploadExternal',
    ])
    expect(publication).toEqual({
      files: [{ id: 'F1', title: 'notes.txt' }],
      channel_id: 'C1',
      initial_comment: 'hello',
    })
  })

  it('exposes unknown publication to OperationsHandler without a second provider send', async () => {
    let requests = 0
    const url = await slackServer((_request, response) => {
      requests += 1
      json(response, { ok: false, error: 'internal_error' })
    })
    const { executeOperation } = setup(url)
    await expect(executeOperation(operation(), [], context().signal)).rejects.toMatchObject({
      outcomeUnknown: true,
      attempts: 1,
    })
    expect(requests).toBe(1)
  })

  it('cancels actual core app HTTP I/O at the operation deadline', async () => {
    const started = deferred()
    const closed = deferred()
    const url = await slackServer((_request, response) => {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.write('{')
      response.on('close', closed.resolve)
      started.resolve()
    })
    const core = new CoreClient({
      baseUrl: url,
      token: 'local-test-core-token',
      requestTimeoutMs: 5000,
    })
    const { executeOperation } = createSlackGateway({ core, workBudget: new WorkByteBudget(1024) })
    const input = { ...operation(), deadlineMs: Date.now() + 100 }
    const pending = executeOperation(input, [], context().signal)
    await started.promise
    expect(await pending).toEqual({ outcome: 'failed' })
    await closed.promise
  })
})
