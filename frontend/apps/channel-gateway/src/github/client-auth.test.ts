import { describe, expect, it, vi } from 'vitest'
import { z } from 'zod'

import { type GitHubAuthentication, GitHubClient } from './client'
import {
  attempt,
  configuration,
  githubFixture,
  json,
  localServer,
  requestBody,
} from './test-support'

async function fixture() {
  const paths: string[] = []
  const state = { tokenCount: 0, viewerID: 'U_bot', name: 'project', owner: 'example' }
  const url = await localServer(async (request, response) => {
    expect(request.headers.accept).toBe('application/vnd.github.v3+json')
    expect(request.headers['x-github-api-version']).toBe('2022-11-28')
    expect(request.headers['user-agent']).toBe('omnara-channel-gateway')
    expect(request.headers['content-type']).toBe('application/json')
    expect(request.headers.authorization).toMatch(/^Bearer /)
    const path = request.url ?? ''
    paths.push(path)
    if (path === '/app/installations/123/access_tokens') {
      expect(await requestBody(request)).toEqual({
        repository_ids: [456],
        permissions: { pull_requests: 'write', issues: 'read', metadata: 'read' },
      })
      state.tokenCount += 1
      json(response, {
        token: `local-${state.tokenCount}`,
        expires_at: new Date(Date.now() + 3_600_000).toISOString(),
      })
    } else if (path === '/repositories/456') {
      json(response, {
        id: 456,
        node_id: 'R_selected',
        name: state.name,
        owner: { login: state.owner },
      })
    } else if (path === '/graphql') {
      json(response, { data: { viewer: { id: state.viewerID, login: 'example[bot]' } } })
    } else if (path.endsWith('/replies')) {
      expect(await requestBody(request)).toEqual({ body: 'Original reply' })
      json(response, { node_id: 'PRRC_published' }, 201)
    } else throw new Error('unexpected native request')
  })
  const authentication: GitHubAuthentication = {}
  return {
    state,
    paths,
    authentication,
    client: () => new GitHubClient(configuration, url, authentication),
  }
}

describe('GitHub scoped authentication and direct fetch', () => {
  it.each(['cleared', 'refreshed'])(
    'retries a late old-token read after auth was concurrently %s',
    async (change) => {
      const authentication: GitHubAuthentication = {}
      let rejectOld: (() => void) | undefined
      let queries = 0
      const native = await githubFixture((_request, response) => {
        queries += 1
        if (queries === 2)
          rejectOld = () => {
            json(response, { message: 'private expired credential' }, 401)
          }
        else json(response, { data: { viewer: { id: 'U_bot', login: 'example[bot]' } } })
      })
      const client = new GitHubClient(configuration, native.url, authentication)
      await client.viewerID(attempt())
      const pending = client
        .query('viewer', {}, z.unknown(), attempt())
        .catch((cause: unknown) => cause)
      await vi.waitFor(() => {
        expect(rejectOld).toBeDefined()
      })
      const refreshed = { value: 'newer-token', expiresAt: Date.now() + 3_600_000 }
      authentication.token = change === 'refreshed' ? refreshed : undefined
      authentication.viewer = change === 'refreshed' ? { token: refreshed, id: 'U_bot' } : undefined
      rejectOld?.()
      const failure = await pending
      expect(failure).toMatchObject({
        code: 'http_rejected',
        retryable: true,
        outcomeUnknown: false,
      })
      expect(JSON.stringify(failure)).not.toContain('private expired credential')
      expect(queries).toBe(2)
      if (change === 'refreshed') {
        expect(authentication.token).toBe(refreshed)
        expect(authentication.viewer?.token).toBe(refreshed)
        await expect(client.query('viewer', {}, z.unknown(), attempt())).resolves.toMatchObject({
          viewer: { id: 'U_bot' },
        })
        expect(native.calls.at(-1)?.authorization).toBe('Bearer newer-token')
        expect(native.calls.filter((call) => call.path.endsWith('/access_tokens'))).toHaveLength(1)
      } else expect(authentication.token).toBeUndefined()
    },
  )

  it.each(['app JWT', 'new installation token'])(
    'returns a safe %s 401 to the existing retry budget without native replay',
    async (failure) => {
      const paths: string[] = []
      const endpoint = await localServer((request, response) => {
        paths.push(request.url ?? '')
        if (failure === 'new installation token' && request.url?.endsWith('/access_tokens')) {
          json(response, {
            token: 'new-token',
            expires_at: new Date(Date.now() + 3_600_000).toISOString(),
          })
        } else json(response, { message: 'private credentials rejected' }, 401)
      })
      const client = new GitHubClient(configuration, endpoint)
      await expect(client.viewerID(attempt())).rejects.toMatchObject({
        code: 'http_rejected',
        retryable: true,
        outcomeUnknown: false,
      })
      expect(paths).toHaveLength(failure === 'app JWT' ? 1 : 2)
    },
  )

  it('refreshes expired tokens and their verified identity while keeping each client repository check fresh', async () => {
    const f = await fixture()
    expect(await f.client().viewerID(attempt())).toBe('U_bot')
    expect(await f.client().viewerID(attempt())).toBe('U_bot')
    expect(f.paths.filter((path) => path === '/repositories/456')).toHaveLength(2)
    expect(f.paths.filter((path) => path === '/graphql')).toHaveLength(1)
    expect(f.state.tokenCount).toBe(1)
    const token = f.authentication.token
    if (!token) throw new Error('missing token')
    token.expiresAt = Date.now() + 30_000
    f.state.viewerID = 'U_current'
    expect(await f.client().viewerID(attempt())).toBe('U_current')
    expect(f.paths.filter((path) => path === '/repositories/456')).toHaveLength(3)
    expect(f.paths.filter((path) => path === '/graphql')).toHaveLength(2)
    expect(f.state.tokenCount).toBe(2)
  })

  it('refreshes the numeric repository address before a REST reply after rename', async () => {
    const f = await fixture()
    const client = f.client()
    await client.viewerID(attempt())
    f.state.name = 'renamed'
    f.state.owner = 'new-owner'
    expect(await client.publishedReply(7, '9007199254740993', 'Original reply', attempt())).toBe(
      'PRRC_published',
    )
    expect(f.paths.slice(-2)).toEqual([
      '/repositories/456',
      '/repos/new-owner/renamed/pulls/7/comments/9007199254740993/replies',
    ])
    expect(f.state.tokenCount).toBe(1)
  })

  it.each(['9007199254740993', '-9007199254740993'])(
    'preserves the former SDK rejection of unsafe integer literal %s',
    async (raw) => {
      const f = await githubFixture((_request, response) => {
        response.writeHead(200, { 'content-type': 'application/json' })
        response.end(`{"data":{"value":${raw}}}`)
      })
      await expect(f.client.query('viewer', {}, z.unknown(), attempt())).rejects.toMatchObject({
        code: 'invalid_response',
        outcomeUnknown: false,
      })
      expect(f.calls.filter((call) => call.path === '/graphql')).toHaveLength(1)
    },
  )

  it('keeps the SDK floating-point exponent behavior without applying an integer-ID rule to floats', async () => {
    const f = await githubFixture((_request, response) => {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end('{"data":{"value":1e30}}')
    })
    await expect(f.client.query('viewer', {}, z.unknown(), attempt())).resolves.toEqual({
      value: 1e30,
    })
  })
})
