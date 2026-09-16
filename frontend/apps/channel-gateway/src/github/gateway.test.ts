import { generateKeyPairSync } from 'node:crypto'

import type { ChannelReadOperation, ChannelSendOperation } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import type { GatewayOperation } from '../operations'
import { createGitHubGateway, githubCapability, type GitHubGatewayOptions } from './gateway'
import { input, operationFixture, options, requestID } from './operations-test-support'
import {
  configuration,
  finding,
  json,
  mutationInputs,
  noPrevious,
  oldCommit,
  pr,
  thread,
} from './test-support'

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
    lookupGitHubReviews: native.core.lookupGitHubReviews.bind(native.core),
    recordGitHubReview: native.core.recordGitHubReview.bind(native.core),
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
  it('uses the admitted read scope to filter private pending history without recording or mutating', async () => {
    const f = await fixture({
      ownership: 'other_agent',
      native: (request, response) => {
        if (!request.query.includes('query GitHubReviewThread(')) return false
        json(response, {
          data: {
            node: {
              ...thread,
              comments: {
                nodes: [
                  finding,
                  { ...finding, id: 'PRRC_public', body: 'Published', state: 'SUBMITTED' },
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
      payload: { coverage: 'partial', messages: [{ content: { text: 'Published' } }] },
    })
    expect(JSON.stringify(result)).not.toContain(finding.body)
    expect(f.lookups[0]?.scope).toEqual({
      request_id: requestID,
      agent_id: scope.agent_id,
      channel_id: scope.channel_id,
    })
    expect(f.records).toEqual([])
    expect(mutationInputs(f.calls)).toEqual([])
  })
  it('runs the prepared scope through native creation, core recording and child result serialization', async () => {
    const f = await fixture()
    const result = await f.executeOperation(operation(), [], signal())
    expect(result).toMatchObject({
      outcome: 'completed',
      payload: {
        publication: 'draft',
        message_channel: 'reply_channel',
        metadata: { review_id: 'PRR_1', commit_id: oldCommit },
      },
    })
    expect(f.events).toEqual(['create', 'record', 'finding'])
    expect(f.records[0]?.scope).toEqual({
      request_id: requestID,
      agent_id: scope.agent_id,
      channel_id: scope.channel_id,
    })
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

  it('preserves a known owned review reference when the finding acknowledgment is lost', async () => {
    const f = await fixture({
      native: (request, response) => {
        if (!request.query.includes('GitHubAddFinding')) return false
        response.destroy()
        return true
      },
    })
    const result = await f.executeOperation(operation(), [], signal())
    expect(result).toEqual({
      outcome: 'unknown',
      payload: {
        code: 'review_operation_unknown',
        metadata: { review_id: 'PRR_1', commit_id: oldCommit },
      },
    })
    expect(mutationInputs(f.calls)).toHaveLength(2)
  })

  it.each(['publish', 'reply'] as const)(
    'preserves owned references with a neutral unknown %s result after a lost acknowledgment',
    async (action) => {
      const document = action === 'publish' ? 'GitHubSubmitReview' : 'GitHubThreadReply'
      const f = await fixture({
        native: (request, response) => {
          if (!request.query.includes(document)) return false
          response.destroy()
          return true
        },
      })
      const send =
        action === 'publish'
          ? { ...input, params: { publish_review: true, review_id: 'PRR_1' } }
          : f.threadInput
      expect(await f.executeOperation(operation(send), [], signal())).toEqual({
        outcome: 'unknown',
        payload: {
          code: 'review_operation_unknown',
          metadata: { review_id: 'PRR_1', commit_id: oldCommit },
        },
      })
      expect(mutationInputs(f.calls)).toHaveLength(1)
      expect(f.lookups[0]?.observations).toEqual([{ review_id: 'PRR_1' }])
    },
  )

  it('keeps a definite publish rejection failed instead of using the unknown code', async () => {
    const f = await fixture({
      native: (request, response) => {
        if (!request.query.includes('GitHubSubmitReview')) return false
        json(response, {
          errors: [{ type: 'UNPROCESSABLE' }],
          data: { submitPullRequestReview: null },
        })
        return true
      },
    })
    const result = await f.executeOperation(
      operation({ ...input, params: { publish_review: true, review_id: 'PRR_1' } }),
      [],
      signal(),
    )
    expect(result.outcome).toBe('failed')
    expect(JSON.stringify(result)).not.toContain('review_operation_unknown')
    expect(mutationInputs(f.calls)).toHaveLength(1)
  })

  it('uses the exact lookup-owned draft ID and refuses a native review change before dispatch', async () => {
    let reads = 0
    const f = await fixture({
      native: (request, response) => {
        if (!request.query.includes('GitHubThreadIdentity')) return false
        reads += 1
        const root =
          reads === 1
            ? finding
            : { ...finding, pullRequestReview: { id: 'PRR_other', state: 'PENDING' } }
        json(response, { data: { node: { ...thread, comments: { nodes: [root] } } } })
        return true
      },
    })
    const result = await f.executeOperation(operation(f.threadInput), [], signal())
    expect(result).toEqual({ outcome: 'failed', payload: { code: 'review_not_owned' } })
    expect(f.lookups[0]?.observations).toEqual([{ review_id: 'PRR_1' }])
    expect(mutationInputs(f.calls)).toEqual([])
  })

  it.each(['comment', 'review'] as const)(
    'never reports published REST reply success when the %s readback is PENDING',
    async (pending) => {
      const f = await fixture({
        native: (request, response) => {
          if (request.query.includes('GitHubThreadIdentity')) {
            json(response, {
              data: {
                node: {
                  ...thread,
                  comments: {
                    nodes: [
                      {
                        ...finding,
                        state: 'SUBMITTED',
                        pullRequestReview: { id: 'PRR_old', state: 'COMMENTED' },
                      },
                    ],
                  },
                },
              },
            })
            return true
          }
          if (request.query.includes('GitHubCommentIdentity')) {
            json(response, {
              data: {
                node: {
                  ...finding,
                  id: 'PRRC_reply',
                  pullRequest: pr,
                  state: pending === 'comment' ? 'PENDING' : 'SUBMITTED',
                  pullRequestReview: { id: 'PRR_other', state: 'PENDING' },
                  replyTo: { id: finding.id },
                },
              },
            })
            return true
          }
          return false
        },
        rest: (_call, response) => {
          json(response, { node_id: 'PRRC_reply' }, 201)
        },
      })
      const result = await f.executeOperation(operation(f.threadInput), [], signal())
      expect(result).toMatchObject({
        outcome: 'unknown',
        payload: { code: 'review_operation_unknown' },
      })
      expect(f.calls.filter((call) => call.path.endsWith('/replies'))).toHaveLength(1)
      expect(mutationInputs(f.calls)).toEqual([])
      expect(JSON.stringify(result)).not.toContain('PRR_other')
    },
  )

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
    expect(f.records).toEqual([])
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
