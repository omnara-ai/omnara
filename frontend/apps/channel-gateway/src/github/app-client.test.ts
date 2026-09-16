import type { JsonBody } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'
import { z } from 'zod'

import { GitHubAppClient } from './app-client'
import { GitHubAPIError } from './protocol'
import { githubRateLimitDelay } from './rate-limit'
import { attempt, configuration, json, localServer, requestBody } from './test-support'

const permissions = { pull_requests: 'write', issues: 'read', metadata: 'read' }
const installation = { id: 123, app_id: 42, suspended_at: null, permissions }
const repository = {
  __typename: 'Repository',
  id: 'R_selected',
  name: 'project',
  owner: { id: 'O_1', login: 'example' },
}
const node = { data: { node: repository } }
interface Reply {
  body: JsonBody
  status?: number
  headers?: Record<string, string>
}
interface Options {
  app?: Reply
  installation?: Reply
  token?: Reply
  nodes?: Reply[]
  memberships?: Reply[]
}

async function fixture(options: Options = {}) {
  const calls: { path: string; method: string; authorization?: string; body: JsonBody | null }[] =
    []
  let graphIndex = 0
  let membershipIndex = 0
  const url = await localServer(async (request, response) => {
    const path = request.url ?? ''
    const body = request.method === 'POST' ? await requestBody(request) : null
    calls.push({
      path,
      method: request.method ?? '',
      authorization: request.headers.authorization,
      body,
    })
    let reply: Reply
    if (path === '/app') reply = options.app ?? { body: { id: 42 } }
    else if (path === '/app/installations/123')
      reply = options.installation ?? { body: installation }
    else if (path === '/app/installations/123/access_tokens')
      reply = options.token ?? {
        body: {
          token: 'metadata-only',
          expires_at: new Date(Date.now() + 3_600_000).toISOString(),
          permissions: { metadata: 'read' },
        },
      }
    else if (path === '/graphql') reply = options.nodes?.[graphIndex++] ?? { body: node }
    else if (path.startsWith('/repos/') && path.endsWith('/installation'))
      reply = options.memberships?.[membershipIndex++] ?? { body: installation }
    else reply = { body: {}, status: 500 }
    for (const [key, value] of Object.entries(reply.headers ?? {})) response.setHeader(key, value)
    json(response, reply.body, reply.status)
  })
  const client = new GitHubAppClient('42', configuration.privateKey, url)
  return {
    calls,
    client,
    observe: async () => {
      const session = await client.prepareInstallation('123', attempt())
      return session ? session.repositoryState('R_selected') : 'disabled'
    },
  }
}

describe('GitHub exact installation membership', () => {
  it('uses fresh metadata-only credentials and always brackets App-JWT membership with exact node identity', async () => {
    const f = await fixture()
    expect(await f.observe()).toBe('active')
    expect(f.calls.map((call) => call.path)).toEqual([
      '/app',
      '/app/installations/123',
      '/app/installations/123/access_tokens',
      '/graphql',
      '/repos/example/project/installation',
      '/graphql',
    ])
    expect(f.calls[2]?.body).toEqual({ permissions: { metadata: 'read' } })
    for (const call of f.calls) {
      if (call.path === '/graphql') {
        expect(call.authorization).toBe('Bearer metadata-only')
        const body = z
          .object({ query: z.string(), variables: z.object({ id: z.string() }) })
          .parse(call.body)
        expect(body.query).toMatch(/^query /)
        expect(body.variables.id).toBe('R_selected')
      } else expect(call.authorization).toMatch(/^Bearer ey/)
    }
    expect(
      f.calls.some((call) =>
        /\/repositories|\/contents|DELETE|PATCH/.test(call.path + call.method),
      ),
    ).toBe(false)
    await f.observe()
    expect(f.calls.filter((call) => call.path.endsWith('/access_tokens'))).toHaveLength(2)
  })

  it('does not mistake public metadata visibility or an unchanged name for selected membership', async () => {
    const f = await fixture({ memberships: [{ body: {}, status: 404 }] })
    expect(await f.observe()).toBe('disabled')
    expect(f.calls.at(-1)?.path).toBe('/graphql')
  })

  const unavailable: Reply[] = [
    { body: { data: { node: null } } },
    { body: { data: { node: null }, errors: [{ type: 'NOT_FOUND', path: ['node'] }] } },
  ]
  it.each(unavailable)(
    'classifies only explicit scoped node absence as unavailable',
    async (reply) => {
      const f = await fixture({ nodes: [reply] })
      expect(await f.observe()).toBe('disabled')
      expect(f.calls.some((call) => call.path.startsWith('/repos/'))).toBe(false)
    },
  )

  const inconclusive: JsonBody[] = [
    {},
    { data: null },
    { data: {} },
    { data: { node: null }, errors: [{ type: 'FORBIDDEN', path: ['node'] }] },
    { data: { node: null }, errors: [{ type: 'NOT_FOUND', path: ['node', 'owner'] }] },
    { data: { node: null }, errors: [{ type: 'NOT_FOUND' }] },
    {
      data: { node: null },
      errors: [
        { type: 'NOT_FOUND', path: ['node'] },
        { type: 'INTERNAL', path: ['node'] },
      ],
    },
    { data: { node: { ...repository, owner: null } } },
    { data: { node: { ...repository, __typename: 'Issue' } } },
    { data: { node: { ...repository, id: 'R_other' } } },
    { data: { node: repository }, errors: [{ type: 'NOT_FOUND', path: ['node'] }] },
  ]
  it.each(inconclusive)(
    'rejects ambiguous/partial/wrong-identity observation %# without disabling',
    async (body) => {
      const f = await fixture({ nodes: [{ body }] })
      await expect(f.observe()).rejects.toBeInstanceOf(GitHubAPIError)
      expect(f.calls.some((call) => call.path.startsWith('/repos/'))).toBe(false)
    },
  )

  it.each([404, 200])(
    'refetches after a rename, including old-name reuse with status %i',
    async (status) => {
      const renamed = { data: { node: { ...repository, name: 'renamed' } } }
      const f = await fixture({
        nodes: [{ body: node }, { body: renamed }, { body: renamed }, { body: renamed }],
        memberships: [{ status, body: { ...installation, id: 999 } }, { body: installation }],
      })
      expect(await f.observe()).toBe('active')
      expect(
        f.calls.filter((call) => call.path.startsWith('/repos/')).map((call) => call.path),
      ).toEqual(['/repos/example/project/installation', '/repos/example/renamed/installation'])
    },
  )

  it('never adopts a different installation after transfer', async () => {
    const transferred = { data: { node: { ...repository, owner: { id: 'O_2', login: 'other' } } } }
    const f = await fixture({
      nodes: [{ body: node }, { body: transferred }, { body: transferred }, { body: transferred }],
      memberships: [{ body: {}, status: 404 }, { body: { ...installation, id: 999 } }],
    })
    expect(await f.observe()).toBe('disabled')
  })

  it('does not follow redirects and stops bounded address churn without claiming absence', async () => {
    const f = await fixture({
      memberships: [
        { body: {}, status: 301, headers: { location: 'http://untrusted.invalid' } },
        { body: {}, status: 301, headers: { location: 'http://untrusted.invalid' } },
      ],
    })
    await expect(f.observe()).rejects.toMatchObject({
      code: 'repository_identity_unstable',
      retryable: true,
    })
    expect(f.calls.filter((call) => call.path.startsWith('/repos/'))).toHaveLength(2)
  })

  it.each([
    { body: {}, status: 404 },
    { body: { ...installation, suspended_at: '2026-09-15T00:00:00Z' } },
    { body: { ...installation, permissions: { ...permissions, pull_requests: 'read' } } },
    { body: { ...installation, permissions: { ...permissions, issues: 'none' } } },
  ] satisfies Reply[])(
    'recognizes current installation unavailability before repository reads %#',
    async (reply) => {
      const f = await fixture({ installation: reply })
      expect(await f.observe()).toBe('disabled')
      expect(f.calls).toHaveLength(2)
    },
  )

  it.each([401, 403, 429, 500])('never treats HTTP %i as repository absence', async (status) => {
    const f = await fixture({
      nodes: [{ body: { message: 'secret provider diagnostic' }, status }],
    })
    const error = await f.observe().catch((cause: unknown) => cause)
    expect(error).toBeInstanceOf(GitHubAPIError)
    expect(error).toMatchObject({ retryable: true })
    expect(JSON.stringify(error)).not.toContain('secret provider diagnostic')
    expect(f.calls.filter((call) => call.path === '/graphql')).toHaveLength(1)
  })

  it.each([403, 429])(
    'exports a bounded provider delay on HTTP %i without sleeping/retrying',
    async (status) => {
      const f = await fixture({
        nodes: [
          {
            body: {},
            status,
            headers: {
              'retry-after': '120',
              'x-ratelimit-remaining': '0',
              'x-ratelimit-reset': String(Math.ceil(Date.now() / 1000) + 600),
            },
          },
        ],
      })
      const error = await f.observe().catch((cause: unknown) => cause)
      expect(error).toMatchObject({ code: 'rate_limited', retryable: true })
      expect(error).toHaveProperty('retryAfterMs', expect.any(Number))
      if (!(error instanceof GitHubAPIError)) throw new Error('missing classified error')
      expect(error.retryAfterMs).toBeGreaterThan(590_000)
      expect(f.calls.filter((call) => call.path === '/graphql')).toHaveLength(1)
    },
  )

  it('honors rate limits reported in a successful GraphQL HTTP response', async () => {
    const f = await fixture({
      nodes: [
        {
          body: { data: { node: null }, errors: [{ type: 'RATE_LIMITED' }] },
          headers: { 'retry-after': '90' },
        },
      ],
    })
    await expect(f.observe()).rejects.toMatchObject({
      code: 'rate_limited',
      retryable: true,
      retryAfterMs: 90_000,
    })
    expect(f.calls.filter((call) => call.path === '/graphql')).toHaveLength(1)
    expect(f.calls.some((call) => call.path.startsWith('/repos/'))).toBe(false)
  })

  it.each(['omitted', 'empty'])(
    'accepts a successful final allowance with %s errors',
    async (errors) => {
      const reply = {
        body: errors === 'empty' ? { ...node, errors: [] } : node,
        headers: {
          'x-ratelimit-remaining': '0',
          'x-ratelimit-reset': String(Math.ceil(Date.now() / 1000) + 120),
        },
      }
      const f = await fixture({ nodes: [reply, reply] })
      expect(await f.observe()).toBe('active')
      expect(f.calls.filter((call) => call.path === '/graphql')).toHaveLength(2)
      expect(f.calls.filter((call) => call.path.startsWith('/repos/'))).toHaveLength(1)
    },
  )

  it('cancels actual native I/O at the shared deadline', async () => {
    let closed = false
    const url = await localServer((_request, response) => {
      response.on('close', () => {
        closed = true
      })
    })
    const client = new GitHubAppClient('42', configuration.privateKey, url)
    await expect(client.prepareInstallation('123', attempt(undefined, 100))).rejects.toMatchObject({
      retryable: true,
    })
    await vi.waitFor(() => {
      expect(closed).toBe(true)
    })
  })
})

describe('GitHub control retry delay', () => {
  it.each([
    [{ 'retry-after': '120' }, 120_000],
    [{ 'retry-after': 'invalid' }, 60_000],
    [{ 'retry-after': '999999999999999999999999' }, 86_400_000],
    [{ 'retry-after': 'Thu, 01 Jan 1970 00:00:00 GMT' }, 0],
    [{ 'x-ratelimit-remaining': '0', 'x-ratelimit-reset': '1600' }, 600_000],
    [{ 'x-ratelimit-remaining': '4999', 'x-ratelimit-reset': '1600' }, 60_000],
    [
      { 'x-ratelimit-remaining': '4999', 'x-ratelimit-reset': '1600', 'retry-after': '120' },
      120_000,
    ],
  ] as const)('keeps provider scheduling finite %#', (headers, expected) => {
    expect(githubRateLimitDelay(new Headers(headers), 1_000_000)).toBe(expected)
  })
})
