import { ApiError, type ChannelOpaqueObject } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'
import { z } from 'zod'

import type { GatewayOperation } from '../operations'
import { SlackAPIError } from './client'
import { installation, scope, setup, suffix } from './gateway-test-support'
import { credentials, deferred, json, slackServer } from './test-support'

function operation(kind: 'resolve_address', payload: ChannelOpaqueObject): GatewayOperation {
  return {
    kind,
    capability: { connector_key: 'omnara', provider: 'slack' },
    requestId: 'request-1',
    deadlineMs: Date.now() + 10_000,
    scope: {
      project_id: scope.project_id,
      integration_app_id: scope.integration_app_id,
      integration_install_id: scope.integration_install_id,
    },
    artifacts: [],
    payloadJSON: JSON.stringify(payload),
  }
}
const context = () => ({ signal: new AbortController().signal })

describe('Slack gateway address resolution', () => {
  it.each([
    { input: { provider_ref: 'not a Slack locator' }, code: 'invalid_address' },
    { input: { provider_ref: 'C1', provider_ref_kind: 'forum' }, code: 'unsupported_address' },
    { input: { provider_ref: '' }, code: 'invalid_address' },
    { input: { provider_ref: 'C1', extra: 'private detail' }, code: 'invalid_address' },
  ])('returns only a fixed code for invalid setup input: $code', async ({ input, code }) => {
    let requests = 0
    const url = await slackServer((_request, response) => {
      requests++
      json(response, { ok: false, error: 'private diagnostic' })
    })
    const { executeOperation, core } = setup(url)
    expect(
      await executeOperation(operation('resolve_address', input), [], context().signal),
    ).toEqual({ outcome: 'failed', payload: { code } })
    expect(requests).toBe(0)
    expect(core.publishDefinition).not.toHaveBeenCalled()
  })

  it.each([
    { response: { ok: false, error: 'channel_not_found' }, code: 'address_unavailable' },
    { response: { ok: false, error: 'not_in_channel' }, code: 'address_unavailable' },
    {
      response: { ok: false, error: 'thread_not_found' },
      code: 'address_unavailable',
      thread: true,
    },
    {
      response: { ok: false, error: 'message_not_found' },
      code: 'address_unavailable',
      thread: true,
    },
    {
      response: { ok: true, channel: { id: 'C1', is_channel: true, context_team_id: 'OTHER' } },
      code: 'address_unavailable',
    },
    { response: { ok: true, channel: { id: 'C1', is_mpim: true } }, code: 'unsupported_address' },
    { response: { ok: false, error: 'missing_scope' } },
    { response: { ok: false, error: 'invalid_auth' } },
    { response: { ok: false, error: 'unknown private error' } },
    { response: { ok: true, channel: {} } },
  ])('classifies only explicit address failures: %j', async ({ response, code, thread }) => {
    let requests = 0
    const url = await slackServer((request, outgoing) => {
      requests++
      json(
        outgoing,
        z
          .json()
          .parse(
            thread && request.url === '/conversations.info'
              ? { ok: true, channel: { id: 'C1', is_channel: true } }
              : response,
          ),
      )
    })
    const { executeOperation, core } = setup(url)
    const input = operation('resolve_address', { provider_ref: thread ? 'C1:100.000001' : 'C1' })
    expect(await executeOperation(input, [], context().signal)).toEqual(
      code ? { outcome: 'failed', payload: { code } } : { outcome: 'failed' },
    )
    expect(requests).toBe(thread ? 2 : 1)
    expect(core.publishDefinition).not.toHaveBeenCalled()
  })

  it.each([new ApiError(404, 'private core diagnostic'), new SlackAPIError('channel_not_found')])(
    'never classifies definition publication errors as missing Slack addresses',
    async (error) => {
      const url = await slackServer((_request, response) => {
        json(response, { ok: true, channel: { id: 'C1', is_channel: true } })
      })
      const { executeOperation, core } = setup(url)
      core.publishDefinition.mockRejectedValue(error)
      expect(
        await executeOperation(
          operation('resolve_address', { provider_ref: 'C1' }),
          [],
          context().signal,
        ),
      ).toEqual({ outcome: 'failed' })
      expect(core.publishDefinition).toHaveBeenCalledOnce()
    },
  )

  it('keeps missing installation context a bare failure before provider I/O', async () => {
    let requests = 0
    const url = await slackServer((_request, response) => {
      requests++
      json(response, { ok: false, error: 'private diagnostic' })
    })
    const { executeOperation, core } = setup(url)
    core.getInstallationConfiguration.mockResolvedValue({
      ...installation,
      install: { ...installation.install, provider_tenant_id: undefined },
    })
    expect(
      await executeOperation(
        operation('resolve_address', { provider_ref: 'C1' }),
        [],
        context().signal,
      ),
    ).toEqual({ outcome: 'failed' })
    expect(requests).toBe(0)
    expect(core.publishDefinition).not.toHaveBeenCalled()
  })

  it('keeps core configuration not-found errors separate from missing provider addresses', async () => {
    const { executeOperation, core } = setup()
    core.getInstallationConfiguration.mockRejectedValue(new ApiError(404, 'private core error'))
    expect(
      await executeOperation(
        operation('resolve_address', { provider_ref: 'C1' }),
        [],
        context().signal,
      ),
    ).toEqual({ outcome: 'failed' })
    expect(core.publishDefinition).not.toHaveBeenCalled()
  })

  it('aborts stalled provider reads without returning an address diagnosis', async () => {
    const started = deferred()
    const closed = deferred()
    const url = await slackServer((_request, response) => {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.write('{')
      response.once('close', closed.resolve)
      started.resolve()
    })
    const { executeOperation, core } = setup(url)
    const input = operation('resolve_address', { provider_ref: 'C1' })
    input.deadlineMs = Date.now() + 150
    const result = executeOperation(input, [], context().signal)
    await started.promise
    expect(await result).toEqual({ outcome: 'failed' })
    await closed.promise
    expect(core.publishDefinition).not.toHaveBeenCalled()
  })

  it('resolves setup addresses with live installation credentials and no agent or provider mutation', async () => {
    const paths: string[] = []
    const url = await slackServer((request, response) => {
      paths.push(request.url ?? '')
      expect(request.headers.authorization).toBe(`Bearer ${credentials.botToken}`)
      json(response, {
        ok: true,
        channel: { id: 'C1', is_channel: true, name: 'engineering', context_team_id: 'T1' },
      })
    })
    const { executeOperation, core } = setup(url)
    const input = operation('resolve_address', { provider_ref: 'C1' })
    expect(await executeOperation(input, [], context().signal)).toEqual({
      outcome: 'completed',
      payload: {
        definition_id: `cdef_${suffix}`,
        provider_ref: 'C1',
        provider_ref_kind: 'channel',
        display_name: 'engineering',
      },
    })
    expect(core.publishDefinition.mock.calls[0]?.[0]).toEqual(input.scope)
    expect(core.lookupRecipients).not.toHaveBeenCalled()
    expect(core.deliverWorkflow).not.toHaveBeenCalled()
    expect(paths).toEqual(['/conversations.info'])
  })

  it('returns failed for resolve transport interruption without publishing a definition', async () => {
    const url = await slackServer((request) => {
      request.socket.destroy()
    })
    const { executeOperation, core } = setup(url)
    const input = operation('resolve_address', { provider_ref: 'C1' })
    input.deadlineMs = Date.now() + 100
    expect(await executeOperation(input, [], context().signal)).toEqual({ outcome: 'failed' })
    expect(core.publishDefinition).not.toHaveBeenCalled()
  })
})
