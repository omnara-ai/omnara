import { randomUUID } from 'node:crypto'

import { ApiError, type ChannelConnectorControlReceipt, schemas } from '@omnara/sdk'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { z } from 'zod'

import { CoreClient } from '../core-client'
import { ReceiptClientError } from '../receipt-http'
import { WorkByteBudget } from '../work-budget'
import { GitHubAppClient } from './app-client'
import { githubLifecycleEvent, projectGitHubEvent } from './events'
import { createGitHubFactory } from './factory'
import {
  app,
  contexts,
  coreFixture,
  installation,
  signedRequest,
  suffix,
} from './inbound-test-support'
import { processGitHubControlReceipt } from './lifecycle'
import { GitHubAPIError } from './protocol'
import { json, localServer, requestBody } from './test-support'

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

function installID(index: number) {
  const digits = 'abcdefghijklmnopqrstuvwxyz234567'
  let tail = ''
  do {
    tail = digits.charAt(index % 32) + tail
    index = Math.floor(index / 32)
  } while (index > 0)
  return `iin_${tail.padStart(26, 'a')}`
}
function controlReceipt(): ChannelConnectorControlReceipt {
  const event = githubLifecycleEvent.parse(
    projectGitHubEvent(
      JSON.stringify({ action: 'removed', installation: { id: 123, app_id: 42 } }),
      'installation_repositories',
      randomUUID(),
    ),
  )
  const receipt: ChannelConnectorControlReceipt = {
    receipt_id: `icrc_${suffix}`,
    integration_app_id: app.app.id,
    provider_tenant_id: '123',
    event_id: event.delivery_id,
    payload: event,
    state: 'processing',
    last_installation_id: null,
    end_installation_id: installID(2),
    lease_token: randomUUID(),
    lease_generation: 1,
    attempts_since_progress: 1,
    lease_expires_at: new Date(Date.now() + 60_000).toISOString(),
    created_at: new Date().toISOString(),
    last_error: {},
  }
  expect(schemas.zChannelConnectorControlReceipt.safeParse(receipt).success).toBe(true)
  return receipt
}
function fixture(count = 2) {
  const core = coreFixture()
  const parent = new WorkByteBudget(256 * 1024 * 1024)
  const budget = new WorkByteBudget(128 * 1024 * 1024, parent)
  const queued = { ...controlReceipt(), end_installation_id: count ? installID(count) : null }
  const sequence: string[] = []
  const native = vi
    .fn<(nodeID: string) => Promise<'active' | 'disabled'>>()
    .mockImplementation(() => {
      sequence.push('native')
      return Promise.resolve('active')
    })
  const prepare = vi
    .spyOn(GitHubAppClient.prototype, 'prepareInstallation')
    .mockImplementation(() => {
      sequence.push('prepare')
      return Promise.resolve({ repositoryState: native })
    })
  const scopes = Array.from({ length: count }, (_, index) => ({
    id: installID(index + 1),
    project_id: installation.install.project_id,
    provider_tenant_id: '123',
    provider_account_ref: String(index + 456),
    provider_identity: { repository_node_id: `R_${index + 1}` },
    state: 'disabled' as const,
    configuration_revision: 8,
  }))
  core.listInstallationControlScopes.mockImplementation((_app, query) => {
    sequence.push('core fence')
    const after =
      query.after_installation_id === undefined
        ? 0
        : scopes.findIndex((scope) => scope.id === query.after_installation_id) + 1
    const end = scopes.findIndex((scope) => scope.id === query.through_installation_id) + 1
    const page = scopes.slice(after, Math.min(after + (query.limit ?? 50), end))
    return Promise.resolve({
      app_configuration_revision: 1,
      installations: page,
      through_installation_id: query.through_installation_id ?? null,
      next_after_installation_id: after + page.length < end ? (page.at(-1)?.id ?? null) : null,
    })
  })
  core.setInstallationProviderState.mockImplementation((_app, _id, body) => {
    sequence.push('apply')
    return Promise.resolve({
      state: body.state,
      configuration_revision: body.expected_configuration_revision + 1,
    })
  })
  const context = {
    deadlineMs: Date.now() + 30_000,
    signal: new AbortController().signal,
    reserveWorkBytes: budget.reserve,
  }
  const run = (receipt = queued) =>
    processGitHubControlReceipt(receipt, context, { core, apiUrl: 'http://127.0.0.1:1' })
  return { core, parent, budget, queued, scopes, sequence, native, prepare, context, run }
}

describe('GitHub durable app control intake', () => {
  it('saves signed delivery provenance before ack without doing inline reconciliation', async () => {
    const core = coreFixture()
    const f = contexts()
    const runtime = await createGitHubFactory({ core }).create(f.factory)
    const raw = JSON.stringify({
      action: 'removed',
      installation: { id: 123, app_id: 42 },
      repositories_removed: [{ id: 456 }],
    })
    const request = signedRequest(raw, 'installation_repositories')
    const saved = projectGitHubEvent(
      raw,
      'installation_repositories',
      request.headers.get('x-github-delivery') ?? '',
    )
    expect((await runtime.handleWebhook(request, f.intake)).status).toBe(202)
    expect(core.submitControlEvent).toHaveBeenCalledWith(
      app.app.id,
      {
        event_id: saved && 'delivery_id' in saved ? saved.delivery_id : '',
        provider_tenant_id: '123',
        payload: saved,
      },
      expect.any(AbortSignal),
    )
    expect(core.listInstallationControlScopes).not.toHaveBeenCalled()
    expect(core.setInstallationProviderState).not.toHaveBeenCalled()
    expect(f.submitInbound).not.toHaveBeenCalled()
    expect(f.budget.usedBytes).toBe(0)
    await runtime.close()
  })
  it('does not acknowledge failed persistence or accept a different native App', async () => {
    const core = coreFixture()
    const f = contexts()
    const runtime = await createGitHubFactory({ core }).create(f.factory)
    core.submitControlEvent.mockRejectedValue(new ReceiptClientError('transport_failed'))
    expect(
      (
        await runtime.handleWebhook(
          signedRequest(
            JSON.stringify({ action: 'deleted', installation: { id: 123, app_id: 42 } }),
            'installation',
          ),
          f.intake,
        )
      ).status,
    ).toBe(503)
    expect(
      (
        await runtime.handleWebhook(
          signedRequest(
            JSON.stringify({ action: 'deleted', installation: { id: 123, app_id: 99 } }),
            'installation',
          ),
          f.intake,
        )
      ).status,
    ).toBe(400)
    expect(core.submitControlEvent).toHaveBeenCalledOnce()
    await runtime.close()
  })
})

describe('GitHub bounded control receipt processing', () => {
  it('retries a transient configuration 404 without retiring the receipt or losing its prefix', async () => {
    const f = fixture()
    const queued = { ...f.queued, last_installation_id: installID(1) }
    f.core.getAppConfiguration.mockRejectedValueOnce(new ApiError(404, 'configuration unavailable'))
    expect(await f.run(queued)).toEqual({
      outcome: 'retry',
      last_error: { code: 'control_observation_inconclusive' },
    })
    expect(queued.last_installation_id).toBe(installID(1))
    expect(f.prepare).not.toHaveBeenCalled()
    expect(f.core.setInstallationProviderState).not.toHaveBeenCalled()
    expect(f.budget.usedBytes).toBe(0)
    expect(await f.run(queued)).toEqual({
      outcome: 'completed',
      last_installation_id: installID(2),
    })
    expect(f.native.mock.calls.map(([id]) => id)).toEqual(['R_2'])
  })
  it('disables every scope of an unavailable installation with original fences and resumable progress', async () => {
    const f = fixture(21)
    const page = f.scopes.slice(0, 20).map((scope, index) => ({
      ...scope,
      state: 'active' as const,
      configuration_revision: 8 + index,
    }))
    f.core.listInstallationControlScopes.mockResolvedValueOnce({
      app_configuration_revision: 1,
      installations: page,
      through_installation_id: installID(21),
      next_after_installation_id: installID(20),
    })
    f.prepare.mockResolvedValue(null)
    expect(await f.run()).toEqual({ outcome: 'yield', last_installation_id: installID(20) })
    expect(f.core.setInstallationProviderState).toHaveBeenCalledTimes(20)
    expect(
      f.core.setInstallationProviderState.mock.calls.map(([appID, id, body]) => ({
        appID,
        id,
        body,
      })),
    ).toEqual(
      page.map((scope) => ({
        appID: app.app.id,
        id: scope.id,
        body: {
          provider_tenant_id: '123',
          provider_account_ref: scope.provider_account_ref,
          expected_app_configuration_revision: 1,
          expected_configuration_revision: scope.configuration_revision,
          state: 'disabled',
        },
      })),
    )
    expect(await f.run({ ...f.queued, last_installation_id: installID(20) })).toEqual({
      outcome: 'completed',
      last_installation_id: installID(21),
    })
    expect(f.core.setInstallationProviderState).toHaveBeenCalledTimes(21)
    expect(f.core.setInstallationProviderState.mock.calls.at(-1)?.[2]).toMatchObject({
      expected_configuration_revision: 8,
      state: 'disabled',
    })
    expect(f.native).not.toHaveBeenCalled()
    expect(f.budget.usedBytes).toBe(0)
  })
  it('restores disabled scopes only from native facts after the original core fences', async () => {
    const f = fixture()
    expect(await f.run()).toEqual({ outcome: 'completed', last_installation_id: installID(2) })
    expect(f.sequence).toEqual(['core fence', 'prepare', 'native', 'apply', 'native', 'apply'])
    expect(f.prepare).toHaveBeenCalledOnce()
    expect(f.core.setInstallationProviderState.mock.calls[0]?.[2]).toMatchObject({
      expected_app_configuration_revision: 1,
      expected_configuration_revision: 8,
      state: 'active',
    })
    expect(f.budget.usedBytes).toBe(0)
    expect(f.parent.usedBytes).toBe(0)
  })
  it('advances beyond 1000 connections across fresh claims without a total attempt ceiling', async () => {
    const f = fixture(1001)
    let queued = { ...f.queued, attempts_since_progress: 1000 }
    let outcome: string | undefined
    for (let claim = 0; claim < 60; claim += 1) {
      const result = await f.run(queued)
      outcome = result.outcome
      if (outcome === 'completed') break
      expect(outcome).toBe('yield')
      expect(result.last_installation_id).toBeDefined()
      queued = {
        ...queued,
        last_installation_id: result.last_installation_id ?? queued.last_installation_id,
        lease_generation: queued.lease_generation + 1,
      }
    }
    expect(outcome).toBe('completed')
    expect(f.native).toHaveBeenCalledTimes(1001)
    expect(new Set(f.core.setInstallationProviderState.mock.calls.map(([, id]) => id)).size).toBe(
      1001,
    )
    expect(f.budget.usedBytes).toBe(0)
  })
  it('checkpoints a confirmed prefix on ambiguous application and re-observes after restart', async () => {
    const f = fixture()
    f.core.setInstallationProviderState
      .mockResolvedValueOnce({ state: 'active', configuration_revision: 9 })
      .mockRejectedValueOnce(new ReceiptClientError('transport_failed'))
    expect(await f.run()).toEqual({
      outcome: 'retry',
      last_installation_id: installID(1),
      last_error: { code: 'control_observation_inconclusive' },
    })
    const before = f.native.mock.calls.length
    expect(
      await f.run({ ...f.queued, last_installation_id: installID(1), lease_generation: 2 }),
    ).toEqual({ outcome: 'completed', last_installation_id: installID(2) })
    expect(f.native.mock.calls.slice(before).map(([nodeID]) => nodeID)).toEqual(['R_2'])
    expect(f.prepare).toHaveBeenCalledTimes(2)
  })
  it('recovers a lost state acknowledgment with fresh native reads and progresses past 1000 over HTTP', async () => {
    const f = fixture(1001)
    f.prepare.mockRestore()
    const nativeInstallation = {
      id: 123,
      app_id: 42,
      suspended_at: null,
      permissions: { pull_requests: 'write', issues: 'read', metadata: 'read' },
    }
    const facts: (Omit<(typeof f.scopes)[number], 'state'> & { state: 'active' | 'disabled' })[] =
      f.scopes.map((scope) => ({ ...scope }))
    let lostResponse = false
    let pages = 0
    let tokens = 0
    const observations: string[] = []
    const applied: { id: string; state: string; revision: number }[] = []
    const url = await localServer(async (request, response) => {
      const address = new URL(request.url ?? '', 'http://local.test')
      const path = address.pathname
      if (path.startsWith('/api/')) {
        expect(request.headers.authorization).toBe('Bearer local-core-token')
        if (path.endsWith('/configuration')) {
          json(response, app)
          return
        }
        if (path.endsWith('/installation-control-scopes')) {
          pages += 1
          const after = address.searchParams.get('after_installation_id')
          const start = after ? facts.findIndex((scope) => scope.id === after) + 1 : 0
          const page = facts.slice(start, start + Number(address.searchParams.get('limit')))
          json(response, {
            app_configuration_revision: 1,
            installations: page,
            through_installation_id: facts.at(-1)?.id ?? null,
            next_after_installation_id:
              start + page.length < facts.length ? (page.at(-1)?.id ?? null) : null,
          })
          return
        }
        const scope = facts.find((scope) =>
          path.endsWith(`/installations/${scope.id}/provider-state`),
        )
        if (!scope) throw new Error('unexpected core path')
        const body = schemas.zSetChannelConnectorInstallationProviderStateRequest.parse(
          await requestBody(request),
        )
        expect(Number(body.expected_configuration_revision)).toBe(scope.configuration_revision)
        expect(Number(body.expected_app_configuration_revision)).toBe(1)
        scope.configuration_revision += 1
        scope.state = body.state
        applied.push({ id: scope.id, state: body.state, revision: scope.configuration_revision })
        if (scope.id === installID(21) && !lostResponse) {
          lostResponse = true
          response.destroy()
          return
        }
        json(response, {
          state: scope.state,
          configuration_revision: scope.configuration_revision,
        })
        return
      }
      if (path === '/app') {
        json(response, { id: 42 })
        return
      }
      if (path === '/app/installations/123') {
        json(response, nativeInstallation)
        return
      }
      if (path === '/app/installations/123/access_tokens') {
        tokens += 1
        expect(await requestBody(request)).toEqual({ permissions: { metadata: 'read' } })
        json(response, {
          token: 'metadata-token',
          expires_at: new Date(Date.now() + 3_600_000).toISOString(),
          permissions: { metadata: 'read' },
        })
        return
      }
      if (path === '/graphql') {
        const body = z
          .object({ variables: z.object({ id: z.string() }) })
          .parse(await requestBody(request))
        observations.push(body.variables.id)
        json(response, {
          data: {
            node: {
              __typename: 'Repository',
              id: body.variables.id,
              name: body.variables.id,
              owner: { id: 'O_1', login: 'example' },
            },
          },
        })
        return
      }
      if (path.startsWith('/repos/example/R_') && path.endsWith('/installation')) {
        json(response, nativeInstallation, lostResponse && path.includes('/R_21/') ? 404 : 200)
        return
      }
      throw new Error('unexpected native path')
    })
    const core = new CoreClient({ baseUrl: `${url}/api/v1`, token: 'local-core-token' })
    let queued = f.queued
    let completed = false
    let retries = 0
    for (let claim = 0; claim < 60; claim += 1) {
      const result = await processGitHubControlReceipt(
        queued,
        {
          ...f.context,
          deadlineMs: Date.now() + 30_000,
        },
        { core, apiUrl: url },
      )
      expect(f.budget.usedBytes).toBe(0)
      if (result.outcome === 'completed') {
        expect(result.last_installation_id).toBe(installID(1001))
        completed = true
        break
      }
      if (result.outcome === 'retry') {
        retries += 1
        expect(queued.last_installation_id).toBe(installID(20))
        expect(result.last_installation_id).toBeUndefined()
      } else expect(result.outcome).toBe('yield')
      queued = {
        ...queued,
        last_installation_id: result.last_installation_id ?? queued.last_installation_id,
        lease_generation: queued.lease_generation + 1,
        lease_expires_at: new Date(Date.now() + 60_000).toISOString(),
      }
    }
    expect(completed).toBe(true)
    expect(retries).toBe(1)
    expect(tokens).toBe(pages)
    expect(tokens).toBe(52)
    expect(applied).toHaveLength(1002)
    expect(applied.filter((item) => item.id === installID(21))).toEqual([
      { id: installID(21), state: 'active', revision: 9 },
      { id: installID(21), state: 'disabled', revision: 10 },
    ])
    expect(observations.filter((id) => id === 'R_21')).toHaveLength(4)
    expect(new Set(applied.map((item) => item.id)).size).toBe(1001)
    expect(f.parent.usedBytes).toBe(0)
  }, 60_000)
  it('returns a partial-page successful yield before its deadline', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    const f = fixture()
    f.core.setInstallationProviderState.mockImplementationOnce((_app, _id, body) => {
      vi.setSystemTime(f.context.deadlineMs - 500)
      return Promise.resolve({ state: body.state, configuration_revision: 9 })
    })
    expect(await f.run()).toEqual({ outcome: 'yield', last_installation_id: installID(1) })
    expect(f.native).toHaveBeenCalledOnce()
  })
  it('keeps an original fence on conflict and never sends the old observation with a refreshed revision', async () => {
    const f = fixture()
    f.core.setInstallationProviderState.mockRejectedValue(new ReceiptClientError('http_error', 409))
    expect(await f.run()).toMatchObject({ outcome: 'retry' })
    expect(f.native).toHaveBeenCalledOnce()
    expect(f.core.listInstallationControlScopes).toHaveBeenCalledOnce()
    expect(f.core.setInstallationProviderState).toHaveBeenCalledOnce()
  })
  it('retries a rotated app revision without losing the previously acknowledged prefix', async () => {
    const f = fixture()
    f.core.getAppConfiguration.mockResolvedValue({
      ...app,
      app: { ...app.app, configuration_revision: 2 },
    })
    expect(await f.run({ ...f.queued, last_installation_id: installID(1) })).toMatchObject({
      outcome: 'retry',
    })
    expect(f.native).not.toHaveBeenCalled()
  })
  it('keeps scope generations independent when another delivery completes', async () => {
    const f = fixture(1)
    const second = controlReceipt()
    second.end_installation_id = f.queued.end_installation_id
    second.receipt_id = `icrc_bbbbbbbbbbbbbbbbbbbbbbbbbb`
    expect((await f.run()).outcome).toBe('completed')
    expect((await f.run(second)).outcome).toBe('completed')
    expect(f.native).toHaveBeenCalledTimes(2)
  })
  it('requires a live app-scoped absence check before skipping a setter 404', async () => {
    const f = fixture(1)
    f.core.setInstallationProviderState.mockRejectedValue(new ReceiptClientError('http_error', 404))
    expect((await f.run()).outcome).toBe('retry')
    f.core.listInstallationControlScopes
      .mockResolvedValueOnce({
        app_configuration_revision: 1,
        installations: f.scopes,
        through_installation_id: f.queued.end_installation_id,
        next_after_installation_id: null,
      })
      .mockResolvedValueOnce({
        app_configuration_revision: 1,
        installations: [],
        through_installation_id: f.queued.end_installation_id,
        next_after_installation_id: null,
      })
    expect(await f.run()).toEqual({ outcome: 'completed', last_installation_id: installID(1) })
  })
  it('propagates rate-limit delay while preserving the last confirmed connection', async () => {
    const f = fixture()
    f.native
      .mockResolvedValueOnce('active')
      .mockRejectedValueOnce(
        new GitHubAPIError('rate_limited', { retryable: true, retryAfterMs: 120_000 }),
      )
    expect(await f.run()).toEqual({
      outcome: 'retry',
      last_installation_id: installID(1),
      retry_after_ms: 120_000,
      last_error: { code: 'rate_limited' },
    })
  })
  it.each([
    { name: 'secondary HTTP403', status: 403, remaining: '4999', errors: [], waitForReset: false },
    {
      name: 'untyped secondary HTTP200',
      status: 200,
      remaining: '4999',
      errors: [{ message: 'You have exceeded a secondary rate limit. PRIVATE' }],
      waitForReset: false,
    },
    {
      name: 'secondary message takes precedence over primary type',
      status: 200,
      remaining: '4999',
      errors: [
        { type: 'RATE_LIMITED', message: 'You have exceeded a secondary rate limit. PRIVATE' },
      ],
      waitForReset: false,
    },
    {
      name: 'untyped exhausted quota',
      status: 200,
      remaining: '0',
      errors: [{ message: 'PRIVATE' }],
      waitForReset: true,
    },
    {
      name: 'typed with remaining omitted',
      status: 200,
      remaining: undefined,
      errors: [{ type: 'RATE_LIMITED' }],
      waitForReset: true,
    },
  ])('keeps its confirmed prefix and provider delay on $name', async (scenario) => {
    const f = fixture()
    f.prepare.mockRestore()
    const reset = Math.ceil(Date.now() / 1000) + 120
    const nativeInstallation = {
      id: 123,
      app_id: 42,
      suspended_at: null,
      permissions: { pull_requests: 'write', issues: 'read', metadata: 'read' },
    }
    const calls: string[] = []
    const url = await localServer(async (request, response) => {
      const path = request.url ?? ''
      calls.push(path)
      if (path === '/app') json(response, { id: 42 })
      else if (path === '/app/installations/123') json(response, nativeInstallation)
      else if (path === '/app/installations/123/access_tokens')
        json(response, {
          token: 'metadata-token',
          permissions: { metadata: 'read' },
          expires_at: new Date(Date.now() + 3_600_000).toISOString(),
        })
      else if (path === '/graphql') {
        const body = z
          .object({ variables: z.object({ id: z.string() }) })
          .parse(await requestBody(request))
        if (body.variables.id === 'R_2' && scenario.status === 200) {
          if (scenario.remaining !== undefined)
            response.setHeader('x-ratelimit-remaining', scenario.remaining)
          response.setHeader('x-ratelimit-reset', String(reset))
          json(response, { data: null, errors: scenario.errors })
          return
        }
        json(response, {
          data: {
            node: {
              __typename: 'Repository',
              id: body.variables.id,
              name: body.variables.id,
              owner: { id: 'O_1', login: 'example' },
            },
          },
        })
      } else if (path === '/repos/example/R_1/installation') json(response, nativeInstallation)
      else if (path === '/repos/example/R_2/installation') {
        response.setHeader('x-ratelimit-remaining', '4999')
        response.setHeader('x-ratelimit-reset', String(reset))
        json(response, { message: 'You have exceeded a secondary rate limit.' }, 403)
      } else throw new Error('unexpected native request')
    })
    const result = await processGitHubControlReceipt(f.queued, f.context, {
      core: f.core,
      apiUrl: url,
    })
    expect(result).toMatchObject({
      outcome: 'retry',
      last_installation_id: installID(1),
      last_error: { code: scenario.status === 200 ? 'rate_limited' : 'provider_state_unavailable' },
    })
    if (scenario.waitForReset) {
      expect(result.retry_after_ms).toBeGreaterThan(115_000)
      expect(result.retry_after_ms).toBeLessThanOrEqual(121_000)
    } else expect(result.retry_after_ms).toBe(60_000)
    expect(JSON.stringify(result)).not.toContain('PRIVATE')
    expect(f.core.setInstallationProviderState).toHaveBeenCalledOnce()
    expect(f.core.setInstallationProviderState.mock.calls[0]?.[1]).toBe(installID(1))
    expect(calls.filter((path) => path === '/graphql')).toHaveLength(3)
    expect(calls.filter((path) => path === '/repos/example/R_2/installation')).toHaveLength(
      scenario.status === 403 ? 1 : 0,
    )
    expect(f.budget.usedBytes).toBe(0)
  })
  it('rejects cross-delivery/native-tenant control work without mutation', async () => {
    const f = fixture()
    expect((await f.run({ ...f.queued, provider_tenant_id: '999' })).outcome).toBe('failed')
    expect((await f.run({ ...f.queued, event_id: randomUUID() })).outcome).toBe('failed')
    expect(f.native).not.toHaveBeenCalled()
    expect(f.core.setInstallationProviderState).not.toHaveBeenCalled()
  })
  it('finishes an empty fixed range without native calls', async () => {
    const f = fixture(0)
    expect(await f.run()).toEqual({ outcome: 'completed' })
    expect(f.native).not.toHaveBeenCalled()
  })
  it('uses only the supplied control child budget through factory wiring', async () => {
    const f = fixture(1)
    const config = contexts()
    const global = vi.fn(() => {
      throw new Error('global budget bypass')
    })
    config.factory.reserveWorkBytes = global
    const runtime = await createGitHubFactory({ core: f.core }).create(config.factory)
    expect(await runtime.processControlReceipt?.(f.queued, f.context)).toMatchObject({
      outcome: 'completed',
    })
    expect(global).not.toHaveBeenCalled()
    expect(f.parent.usedBytes).toBe(0)
    await runtime.close()
  })
})
