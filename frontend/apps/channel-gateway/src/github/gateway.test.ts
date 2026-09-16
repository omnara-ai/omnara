import { generateKeyPairSync } from 'node:crypto'

import type { ChannelReadOperation, ChannelSendOperation } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import type { GatewayOperation } from '../operations/handler'
import { createGitHubGateway, githubCapability, type GitHubGatewayOptions } from './gateway'
import { input, operationFixture, options, requestID } from './operations-test-support'
import { configuration, finding, json, mutationInputs, noPrevious, thread } from './test-support'

const suffix = 'aaaaaaaaaaaaaaaaaaaaaaaaaa'
const scope = {
  project_id: `proj_${suffix}`,
  integration_app_id: `iapp_${suffix}`,
  integration_install_id: `iin_${suffix}`,
  agent_id: `agt_${suffix}`,
  channel_id: `itgt_${suffix}`,
}

async function fixture(settings?: Parameters<typeof operationFixture>[0]) {
  const native = await operationFixture(settings)
  const app = {
    app: {
      id: scope.integration_app_id,
      provider: 'github',
      connector_key: 'omnara',
      provider_app_ref: '42',
      display_name: 'Fixture',
      provider_config: {},
      provider_metadata: {},
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
  const installation = {
    integration_app_id: app.app.id,
    app_configuration_revision: 1,
    install: {
      id: scope.integration_install_id,
      project_id: scope.project_id,
      provider_tenant_id: '123',
      provider_account_ref: '456',
      display_name: 'Fixture',
      provider_config: {},
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
  const core = {
    getAppConfiguration: vi
      .fn<GitHubGatewayOptions['core']['getAppConfiguration']>()
      .mockResolvedValue(app),
    getInstallationConfiguration: vi
      .fn<GitHubGatewayOptions['core']['getInstallationConfiguration']>()
      .mockResolvedValue(installation),
    publishDefinition: vi
      .fn<GitHubGatewayOptions['core']['publishDefinition']>()
      .mockImplementation((_scope, body) => Promise.resolve({ ...body, id: `cdef_${suffix}` })),
  }
  const gateway = createGitHubGateway({ core, apiUrl: native.url })
  return { ...native, ...gateway, core, app, installation }
}

function operation(
  payload: ChannelSendOperation | ChannelReadOperation = input,
  kind: 'send' | 'read' = 'send',
): Extract<GatewayOperation, { kind: 'send' | 'read' | 'interaction' }> {
  return {
    kind,
    scope,
    capability: githubCapability,
    requestId: requestID,
    deadlineMs: options().deadlineMs,
    payloadJSON: JSON.stringify(payload),
    artifacts: [],
  }
}

const signal = () => new AbortController().signal

describe('GitHub gateway operation composition', () => {
  it('publishes the generic child definition before native creation through the real operation envelope', async () => {
    const f = await fixture({
      rest: (_call, response) => {
        expect(f.core.publishDefinition).toHaveBeenCalledWith(
          scope,
          expect.objectContaining({
            implementation_key: 'github_review_thread',
            kind: 'GITHUB_REVIEW_THREAD',
          }),
          expect.any(AbortSignal),
        )
        json(response, { node_id: finding.id }, 201)
        return true
      },
    })
    const result = await f.executeOperation(operation(), [], signal())
    expect(result).toMatchObject({
      outcome: 'completed',
      payload: {
        publication: 'published',
        message_channel: 'reply_channel',
        message_id: finding.id,
        reply_channel: { provider_metadata: { thread_id: thread.id } },
      },
    })
    expect(f.events).toEqual(['comment'])
    expect(f.core.getAppConfiguration).toHaveBeenCalledWith(
      scope.integration_app_id,
      expect.any(AbortSignal),
    )
    expect(f.core.getInstallationConfiguration).toHaveBeenCalledWith(
      scope.integration_app_id,
      scope.integration_install_id,
      expect.any(AbortSignal),
    )
  })

  it('omits all private drafts from a scoped read without ownership callbacks', async () => {
    const f = await fixture({
      native: (request, response) => {
        if (!request.query.includes('query GitHubReviewThread(')) return false
        json(response, {
          data: {
            node: {
              ...thread,
              comments: {
                nodes: [
                  { ...finding, id: 'PRRC_private', body: 'Private draft', state: 'PENDING' },
                  finding,
                ],
                pageInfo: noPrevious,
              },
            },
          },
        })
        return true
      },
    })
    const result = await f.executeOperation(
      operation({ destination: f.threadInput.destination, limit: 2 }, 'read'),
      [],
      signal(),
    )
    expect(result).toMatchObject({
      outcome: 'completed',
      payload: { coverage: 'partial', messages: [{ message_id: finding.id }] },
    })
    expect(JSON.stringify(result)).not.toContain('Private draft')
    expect(f.events).toEqual([])
  })

  it('carries an uncertain send through the shared generic error contract', async () => {
    const f = await fixture({
      rest: (_call, response) => {
        response.destroy()
        return true
      },
    })
    await expect(f.executeOperation(operation(), [], signal())).rejects.toMatchObject({
      outcomeUnknown: true,
      attempts: 1,
    })
    expect(f.events).toEqual(['comment'])
  })

  it.each(['app', 'install', 'project', 'revision', 'capability'] as const)(
    'rejects mismatched %s scope before native calls',
    async (mismatch) => {
      const f = await fixture()
      if (mismatch === 'app')
        f.core.getAppConfiguration.mockResolvedValue({
          ...f.app,
          app: { ...f.app.app, id: `iapp_${'b'.repeat(26)}` },
        })
      if (mismatch === 'capability')
        f.core.getAppConfiguration.mockResolvedValue({
          ...f.app,
          app: { ...f.app.app, connector_key: 'other' },
        })
      if (mismatch === 'install')
        f.core.getInstallationConfiguration.mockResolvedValue({
          ...f.installation,
          install: { ...f.installation.install, id: `iin_${'b'.repeat(26)}` },
        })
      if (mismatch === 'project')
        f.core.getInstallationConfiguration.mockResolvedValue({
          ...f.installation,
          install: { ...f.installation.install, project_id: `proj_${'b'.repeat(26)}` },
        })
      if (mismatch === 'revision')
        f.core.getInstallationConfiguration.mockResolvedValue({
          ...f.installation,
          app_configuration_revision: 2,
        })
      expect(await f.executeOperation(operation(), [], signal())).toEqual({ outcome: 'failed' })
      expect(f.calls).toEqual([])
    },
  )

  it('rejects duplicate payload keys, undeclared message fields and attachments before config/provider I/O', async () => {
    const f = await fixture()
    const base = operation()
    const invalid = [
      { ...base, payloadJSON: base.payloadJSON.replace('"params":', '"params":{},"params":') },
      {
        ...base,
        payloadJSON: JSON.stringify({ ...input, message: { text: 'x', token: 'private' } }),
      },
      { ...base, artifacts: [{ id: 'art', filename: 'x', content_type: 'text/plain' }] },
    ]
    for (const request of invalid)
      expect(await f.executeOperation(request, [], signal())).toEqual({ outcome: 'failed' })
    expect(f.core.getAppConfiguration).not.toHaveBeenCalled()
    expect(f.calls).toEqual([])
  })

  it('keeps interactions in the dashboard and rejects malformed resolution before provider I/O', async () => {
    const f = await fixture()
    const base = operation()
    expect(await f.executeOperation({ ...base, kind: 'interaction' }, [], signal())).toEqual({
      outcome: 'failed',
    })
    expect(
      await f.executeOperation(
        {
          ...base,
          kind: 'resolve_address',
          scope: {
            project_id: scope.project_id,
            integration_app_id: scope.integration_app_id,
            integration_install_id: scope.integration_install_id,
          },
        },
        [],
        signal(),
      ),
    ).toEqual({ outcome: 'failed', payload: { code: 'invalid_address' } })
    expect(f.core.getAppConfiguration).not.toHaveBeenCalled()
    expect(f.calls).toEqual([])
  })

  it('resolves a PR through live configuration and native identity with no agent scope or mutation', async () => {
    const f = await fixture()
    const base = operation()
    const result = await f.executeOperation(
      {
        ...base,
        kind: 'resolve_address',
        payloadJSON: JSON.stringify({ provider_ref: 'repo:456:pr:7' }),
        scope: {
          project_id: scope.project_id,
          integration_app_id: scope.integration_app_id,
          integration_install_id: scope.integration_install_id,
        },
      },
      [],
      signal(),
    )
    expect(result).toMatchObject({
      outcome: 'completed',
      payload: { provider_ref: 'repo:456:pr:7', provider_ref_kind: 'pr' },
    })
    expect(f.core.publishDefinition).toHaveBeenCalledOnce()
    expect(mutationInputs(f.calls)).toEqual([])
  })

  it('passes the original deadline signal through configuration I/O and performs no native request after expiry', async () => {
    const f = await fixture()
    f.core.getAppConfiguration.mockImplementation(
      (_id, signal) =>
        new Promise((_resolve, reject) => {
          if (!signal) throw new Error('missing deadline')
          signal.addEventListener(
            'abort',
            () => {
              reject(new Error('expired'))
            },
            { once: true },
          )
        }),
    )
    expect(
      await f.executeOperation({ ...operation(), deadlineMs: Date.now() + 25 }, [], signal()),
    ).toEqual({ outcome: 'failed' })
    expect(f.calls).toEqual([])
  })

  it('fetches configuration again after rotation instead of retaining an old app token', async () => {
    const f = await fixture()
    const timeline = { ...input, params: {} }
    expect((await f.executeOperation(operation(timeline), [], signal())).outcome).toBe('completed')
    const replacement = generateKeyPairSync('rsa', { modulusLength: 2048 })
      .privateKey.export({ type: 'pkcs8', format: 'pem' })
      .toString()
    f.core.getAppConfiguration.mockResolvedValue({
      ...f.app,
      app: { ...f.app.app, configuration_revision: 2 },
      credential: {
        ...f.app.credential,
        payload: { ...f.app.credential.payload, private_key: replacement },
      },
    })
    f.core.getInstallationConfiguration.mockResolvedValue({
      ...f.installation,
      app_configuration_revision: 2,
    })
    expect((await f.executeOperation(operation(timeline), [], signal())).outcome).toBe('completed')
    expect(f.core.getAppConfiguration).toHaveBeenCalledTimes(2)
    const tokens = f.calls.filter((call) => call.path.endsWith('/access_tokens'))
    expect(tokens).toHaveLength(2)
    expect(tokens[1]?.authorization).not.toBe(tokens[0]?.authorization)
  })
})
