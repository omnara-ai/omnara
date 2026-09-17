import { verify } from 'node:crypto'

import { describe, expect, it } from 'vitest'
import { z } from 'zod'

import { retryOperation } from '../operations/retry'
import { GitHubClient } from './client'
import { postGitHubTimelineComment } from './messages'
import {
  attempt,
  configuration,
  githubFixture,
  json,
  localServer,
  publicKey,
  requestBody,
  restComment,
} from './test-support'

describe('GitHub SDK and native transport', () => {
  it('preserves reset guidance for a GraphQL HTTP 200 read throttle', async () => {
    const reset = Math.ceil(Date.now() / 1000) + 120
    const f = await githubFixture((_request, response) => {
      response.setHeader('x-ratelimit-remaining', '0')
      response.setHeader('x-ratelimit-reset', String(reset))
      json(response, {
        data: null,
        errors: [{ type: 'RATE_LIMITED', message: 'API rate limit exceeded' }],
      })
    })
    const failure = await f.client
      .query('viewer', {}, z.unknown(), attempt())
      .catch((cause: unknown) => cause)
    expect(failure).toMatchObject({ code: 'rate_limited', retryable: true, outcomeUnknown: false })
    const delay = z.object({ retryAfterMs: z.number() }).parse(failure).retryAfterMs
    expect(delay).toBeGreaterThan(119_000)
    expect(delay).toBeLessThanOrEqual(121_000)
    expect(f.calls.map((call) => call.path)).toEqual([
      '/app/installations/123/access_tokens',
      '/repositories/456',
      '/graphql',
    ])
    expect(JSON.stringify(failure)).not.toContain('API rate limit exceeded')
  })
  it.each([
    { name: 'typed', errors: [{ type: 'RATE_LIMITED' }], remaining: undefined },
    {
      name: 'secondary message',
      errors: [{ message: 'You have exceeded a secondary rate limit. PRIVATE' }],
      remaining: '4999',
    },
    { name: 'untyped exhausted quota', errors: [{ message: 'PRIVATE' }], remaining: '0' },
  ])('classifies $name GraphQL read limits without hidden retries', async (scenario) => {
    const f = await githubFixture((_request, response) => {
      response.setHeader('retry-after', '1')
      if (scenario.remaining !== undefined)
        response.setHeader('x-ratelimit-remaining', scenario.remaining)
      json(response, { data: null, errors: scenario.errors })
    })
    const failure = await f.client
      .query('viewer', {}, z.unknown(), attempt())
      .catch((cause: unknown) => cause)
    expect(failure).toMatchObject({ outcomeUnknown: false, retryable: true, retryAfterMs: 1000 })
    expect(JSON.stringify(failure)).not.toContain('PRIVATE')
    expect(f.calls.filter((call) => call.path === '/graphql')).toHaveLength(1)
  })
  it.each(['omitted', 'empty'])(
    'accepts a successful final allowance with %s errors',
    async (errors) => {
      const f = await githubFixture((_request, response) => {
        response.setHeader('x-ratelimit-remaining', '0')
        response.setHeader('x-ratelimit-reset', String(Math.ceil(Date.now() / 1000) + 120))
        const data = { viewer: { login: 'fixture[bot]' } }
        json(response, errors === 'empty' ? { data, errors: [] } : { data })
      })
      await expect(f.client.query('viewer', {}, z.unknown(), attempt())).resolves.toEqual({
        viewer: { login: 'fixture[bot]' },
      })
      expect(f.calls.filter((call) => call.path === '/graphql')).toHaveLength(1)
    },
  )
  it('refreshes a rejected cached token only on the next explicit call', async () => {
    let calls = 0
    const fixture = await githubFixture((_request, response) => {
      calls++
      json(
        response,
        calls === 1
          ? { message: 'expired token' }
          : { data: { viewer: { login: 'fixture[bot]' } } },
        calls === 1 ? 401 : 200,
      )
    })
    await expect(fixture.client.query('viewer', {}, z.unknown(), attempt())).rejects.toMatchObject({
      code: 'http_rejected',
      outcomeUnknown: false,
      retryable: true,
    })
    expect(calls).toBe(1)
    await expect(fixture.client.query('viewer', {}, z.unknown(), attempt())).resolves.toMatchObject(
      { viewer: { login: 'fixture[bot]' } },
    )
    expect(fixture.calls.filter((call) => call.path.endsWith('/access_tokens'))).toHaveLength(2)
  })
  it('signs an App JWT, restricts tokens to one repo and reuses an unexpired token', async () => {
    const fixture = await githubFixture((_request, response) => {
      json(response, { data: { viewer: { login: 'fixture[bot]' } } })
    })
    const schema = z.object({ viewer: z.object({ login: z.string() }) })
    await fixture.client.query('viewer', {}, schema, attempt())
    await fixture.client.query('viewer', {}, schema, attempt())
    expect(fixture.calls.map((call) => call.path)).toEqual([
      '/app/installations/123/access_tokens',
      '/repositories/456',
      '/graphql',
      '/graphql',
    ])
    expect(fixture.calls[0]?.body).toEqual({
      repository_ids: [456],
      permissions: {
        pull_requests: 'write',
        issues: 'read',
        metadata: 'read',
      },
    })
    const jwt = fixture.calls[0]?.authorization?.replace(/^Bearer /, '') ?? ''
    const [header, payload, signature = ''] = jwt.split('.')
    expect(
      verify(
        'RSA-SHA256',
        Buffer.from(`${header}.${payload}`),
        publicKey,
        Buffer.from(signature, 'base64url'),
      ),
    ).toBe(true)
    expect(
      z
        .object({ iss: z.union([z.string(), z.number()]) })
        .parse(JSON.parse(Buffer.from(payload ?? '', 'base64url').toString()))
        .iss.toString(),
    ).toBe('42')
    expect(
      fixture.calls
        .slice(1)
        .every((call) => call.authorization === 'Bearer local-installation-token'),
    ).toBe(true)
  })

  it.each([457, 9_007_199_254_740_992])(
    'rejects wrong or unsafe REST repository IDs before GraphQL: %s',
    async (id) => {
      let graphCalls = 0
      const url = await localServer((request, response) => {
        if (request.url?.endsWith('/access_tokens'))
          json(response, {
            token: 'local-token',
            expires_at: new Date(Date.now() + 3_600_000).toISOString(),
          })
        else if (request.url === '/repositories/456')
          json(response, {
            id,
            node_id: 'R_selected',
            owner: { login: 'example' },
            name: 'project',
          })
        else {
          graphCalls++
          json(response, {})
        }
      })
      await expect(
        new GitHubClient(configuration, url).query('viewer', {}, z.unknown(), attempt()),
      ).rejects.toMatchObject({ outcomeUnknown: false })
      expect(graphCalls).toBe(0)
    },
  )

  it('checks original REST ID bytes before numeric decoding', async () => {
    const url = await localServer((request, response) => {
      if (request.url?.endsWith('/access_tokens')) {
        json(response, {
          token: 'local-token',
          expires_at: new Date(Date.now() + 3_600_000).toISOString(),
        })
      } else {
        response.writeHead(200, { 'content-type': 'application/json' })
        response.end(
          '{"id":456.00000000000000001,"node_id":"R_selected","name":"project","owner":{"login":"example"}}',
        )
      }
    })
    await expect(
      new GitHubClient(configuration, url).query('viewer', {}, z.unknown(), attempt()),
    ).rejects.toMatchObject({ code: 'repository_scope_mismatch' })
  })

  it('rotates expired tokens before a subsequent request without broadening permissions', async () => {
    let tokenCalls = 0
    const url = await localServer(async (request, response) => {
      if (request.url?.endsWith('/access_tokens')) {
        const body = await requestBody(request)
        expect(body).toMatchObject({ repository_ids: [456] })
        tokenCalls++
        // Near-expiry token cannot be accepted or cached.
        json(response, {
          token: `local-${tokenCalls}`,
          expires_at: new Date(Date.now() + (tokenCalls === 1 ? 1000 : 3_600_000)).toISOString(),
        })
      } else if (request.url === '/repositories/456')
        json(response, {
          id: 456,
          node_id: 'R_selected',
          owner: { login: 'example' },
          name: 'project',
        })
      else json(response, { data: { viewer: { login: 'fixture[bot]' } } })
    })
    const client = new GitHubClient(configuration, url)
    await expect(client.query('viewer', {}, z.unknown(), attempt())).rejects.toMatchObject({
      code: 'invalid_response',
    })
    await expect(client.query('viewer', {}, z.unknown(), attempt())).resolves.toMatchObject({
      viewer: { login: 'fixture[bot]' },
    })
    expect(tokenCalls).toBe(2)
  })

  it.each([401, 403, 429, 503])(
    'makes exactly one mutation attempt on HTTP %s, with safe diagnostics',
    async (status) => {
      let mutations = 0
      const { client } = await githubFixture(
        () => {
          throw new Error('unexpected GraphQL')
        },
        (_call, response) => {
          mutations++
          if (status === 429) response.setHeader('retry-after', '60')
          if (status === 403) response.setHeader('x-ratelimit-remaining', '4999')
          json(response, { message: 'DO-NOT-LOG-TOKEN-OR-BODY' }, status)
        },
      )
      await expect(postGitHubTimelineComment(client, 7, 'hello', attempt())).rejects.toMatchObject({
        code: status === 429 ? 'rate_limited' : 'http_rejected',
        outcomeUnknown: status === 503,
        retryable: status === 429,
      })
      expect(mutations).toBe(1)
    },
  )

  it.each(['retry-after', 'x-ratelimit-reset'])(
    'honors GitHub 403 rate guidance from %s without a hidden retry',
    async (header) => {
      let mutations = 0
      const { client } = await githubFixture(
        () => {
          throw new Error('unexpected GraphQL')
        },
        (_call, response) => {
          mutations++
          if (header === 'retry-after') response.setHeader(header, '60')
          else {
            response.setHeader('x-ratelimit-remaining', '0')
            response.setHeader(header, String(Math.ceil(Date.now() / 1000) + 60))
          }
          json(response, { message: 'private details' }, 403)
        },
      )
      const failure = await postGitHubTimelineComment(client, 7, 'hello', attempt()).catch(
        (cause: unknown) => cause,
      )
      expect(failure).toMatchObject({
        code: 'rate_limited',
        retryable: true,
        outcomeUnknown: false,
      })
      expect(
        z.object({ retryAfterMs: z.number() }).parse(failure).retryAfterMs,
      ).toBeGreaterThanOrEqual(59_000)
      expect(mutations).toBe(1)
    },
  )

  it('never follows a provider redirect with the credential', async () => {
    let redirected = 0
    const other = await localServer((_request, response) => {
      redirected++
      json(response, {})
    })
    const { client } = await githubFixture(
      () => {
        throw new Error('unexpected GraphQL')
      },
      (_call, response) => {
        response.writeHead(307, { location: other })
        response.end()
      },
    )
    await expect(postGitHubTimelineComment(client, 7, 'hello', attempt())).rejects.toMatchObject({
      code: 'http_rejected',
    })
    expect(redirected).toBe(0)
    expect(() => new GitHubClient(configuration, 'http://attacker.example')).toThrow(
      'invalid_configuration',
    )
    expect(() => new GitHubClient(configuration, 'https://user:secret@api.github.com')).toThrow(
      'invalid_configuration',
    )
  })

  it.each(['{"data":{},"data":{}}', '\u0000'])(
    'rejects malformed JSON without replaying an ambiguous mutation',
    async (raw) => {
      let mutations = 0
      const { client } = await githubFixture(
        () => {
          throw new Error('unexpected GraphQL')
        },
        (_call, response) => {
          mutations++
          response.writeHead(201, { 'content-type': 'application/json' })
          response.end(raw)
        },
      )
      await expect(
        retryOperation(attempt(), (context) =>
          postGitHubTimelineComment(client, 7, 'hello', context),
        ),
      ).rejects.toMatchObject({ code: 'outcome_unknown', outcomeUnknown: true, attempts: 1 })
      expect(mutations).toBe(1)
    },
  )

  it('caps a valid success comment before decoding even when only unused fields are oversized', async () => {
    const f = await githubFixture(
      () => {
        throw new Error('unexpected GraphQL')
      },
      (_call, response) => {
        json(response, { ...restComment, unused: 'x'.repeat(1024 * 1024) }, 201)
      },
    )
    await expect(postGitHubTimelineComment(f.client, 7, 'hello', attempt())).rejects.toMatchObject({
      code: 'response_too_large',
      outcomeUnknown: true,
      retryable: false,
    })
    expect(f.calls.filter((call) => call.path.endsWith('/comments'))).toHaveLength(1)
  })

  it('aborts a stalled mutation body under the operation deadline', async () => {
    let mutations = 0
    const { client } = await githubFixture(
      () => {
        throw new Error('unexpected GraphQL')
      },
      (_call, response) => {
        mutations++
        response.writeHead(201, { 'content-type': 'application/json' })
        response.write('{"data":')
      },
    )
    // Authenticate before starting the deliberately short mutation deadline.
    await import('./messages').then(({ getGitHubPullRequest }) =>
      getGitHubPullRequest(client, 7, attempt()),
    )
    await expect(
      retryOperation(attempt(undefined, 100), (context) =>
        postGitHubTimelineComment(client, 7, 'hello', context),
      ),
    ).rejects.toMatchObject({ outcomeUnknown: true, attempts: 1 })
    expect(mutations).toBe(1)
  })
})
