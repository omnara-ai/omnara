import { createOmnaraClient, OAUTH_DEVICE_GRANT_TYPE } from '@omnara/sdk'
import type { zCurrentUserOrg } from '@omnara/sdk/zod'
import { Command } from 'commander'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as z from 'zod'

import type { CliConfig } from './config.ts'
import type { ConfigFile, ConfigStore } from './config-file.ts'
import { type LoginReporter, loginTokenName, loginWithDevice } from './device-login.ts'
import { registerLoginCommand } from './login.ts'

const apiUrl = 'https://self-hosted.example/api/v1'
const issuerUrl = 'https://self-hosted.example'
const tokenEndpoint = 'https://self-hosted.example/api/auth/device/token'
const token = 'omnara_pat_v1_test'
const orgId = `org_${'a'.repeat(26)}`
const projectId = `proj_${'a'.repeat(26)}`

class MemoryConfigStore implements ConfigStore {
  readonly path = '/tmp/omnara-config.json'
  readonly updates: Partial<ConfigFile>[] = []
  file: ConfigFile

  constructor(initial: ConfigFile = {}) {
    this.file = initial
  }

  read(): ConfigFile {
    return this.file
  }

  update(patch: Partial<ConfigFile>): ConfigFile {
    this.updates.push(patch)
    this.file = Object.fromEntries(
      Object.entries({ ...this.file, ...patch }).filter(([, value]) => value !== undefined),
    )
    return this.file
  }
}

class UnwritableConfigStore extends MemoryConfigStore {
  override update(): ConfigFile {
    throw new Error('config is not writable')
  }
}

interface RecordedRequest {
  method: string
  url: string
  body: string
  authorization: string | null
}

type CurrentUserOrg = z.output<typeof zCurrentUserOrg>

const zJsonBody = z.json()

function jsonResponse(status: number, body: z.output<typeof zJsonBody>): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

function currentUser(orgs: CurrentUserOrg[]): Response {
  return jsonResponse(200, {
    user: {
      id: `usr_${'a'.repeat(26)}`,
      email: 'person@example.com',
      display_name: 'Person',
    },
    orgs,
  })
}

interface FakeServer {
  fetch: typeof fetch
  requests: RecordedRequest[]
}

function fakeServer(
  me: () => Promise<Response> = () => Promise.resolve(currentUser([])),
): FakeServer {
  const requests: RecordedRequest[] = []
  const fetchImpl: typeof fetch = async (input, init) => {
    const request = input instanceof Request ? input.clone() : new Request(input, init)
    requests.push({
      method: request.method,
      url: request.url,
      body: await request.text(),
      authorization: request.headers.get('authorization'),
    })
    const { pathname } = new URL(request.url)
    if (pathname === '/.well-known/oauth-authorization-server') {
      return jsonResponse(200, {
        issuer: issuerUrl,
        device_authorization_endpoint: `${issuerUrl}/api/auth/device/code`,
        token_endpoint: tokenEndpoint,
        grant_types_supported: [OAUTH_DEVICE_GRANT_TYPE],
        token_endpoint_auth_methods_supported: ['none'],
      })
    }
    if (pathname === '/api/auth/device/code') {
      return jsonResponse(200, {
        device_code: 'device-code',
        user_code: 'ABCDE-FGHIJ',
        verification_uri: '/device',
        verification_uri_complete: '/device?user_code=ABCDE-FGHIJ',
        expires_in: 900,
        interval: 5,
      })
    }
    if (pathname === '/api/auth/device/token') {
      return jsonResponse(200, { access_token: token, token_type: 'bearer' })
    }
    if (pathname === '/api/v1/me') return me()
    return jsonResponse(404, { error: 'not_found' })
  }
  return { fetch: fetchImpl, requests }
}

const silentReporter: LoginReporter = {
  showCode: () => undefined,
  startWaiting: () => undefined,
  stopWaiting: () => undefined,
  success: () => undefined,
  info: () => undefined,
  warn: () => undefined,
  finish: () => undefined,
}

function testConfig(store: ConfigStore, server: FakeServer): CliConfig {
  return {
    client: createOmnaraClient({ baseUrl: apiUrl, fetch: server.fetch }),
    apiUrl,
    issuerUrl,
    store,
    fetch: server.fetch,
    sleep: () => Promise.resolve(),
    ensureLoggedIn: () => Promise.resolve(),
  }
}

async function runLogin(cli: CliConfig): Promise<void> {
  const program = new Command()
  registerLoginCommand(program, cli)
  await program.parseAsync(['node', 'omnara', 'login', '--no-browser'])
}

describe('loginTokenName', () => {
  it('preserves an explicit valid name exactly', () => {
    expect(loginTokenName('CLI on R&D workstation', 'ignored')).toBe('CLI on R&D workstation')
  })

  it('rejects an invalid explicit name', () => {
    expect(() => loginTokenName(' CLI token ', 'ignored')).toThrow(
      'Resource name must not start or end with whitespace',
    )
  })

  it('uses a valid fallback when the generated hostname name is invalid', () => {
    expect(loginTokenName(undefined, 'x'.repeat(64))).toBe('Omnara CLI')
  })
})

describe('loginWithDevice', () => {
  it('drops saved defaults the new account cannot access', async () => {
    const store = new MemoryConfigStore({ org_id: orgId, project_id: projectId })
    const server = fakeServer()
    const warnings: string[] = []

    const result = await loginWithDevice({
      apiUrl,
      issuerUrl,
      browser: false,
      report: {
        ...silentReporter,
        warn: (message) => {
          warnings.push(message)
        },
      },
      store,
      fetch: server.fetch,
      sleep: () => Promise.resolve(),
    })

    expect(warnings).toEqual([
      'Cleared the saved default organization and project: this account cannot access them.',
    ])
    expect(result).toEqual({
      token,
      orgId: undefined,
      projectId: undefined,
      hasOrganizations: false,
    })
    expect(store.updates).toContainEqual({ org_id: undefined, project_id: undefined })
    expect(store.file).toEqual({ token, api_url: apiUrl, issuer_url: issuerUrl })
  })
})

describe('login', () => {
  beforeEach(() => {
    vi.spyOn(console, 'log').mockImplementation(() => undefined)
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
  })

  afterEach(() => {
    vi.restoreAllMocks()
    process.exitCode = undefined
  })

  it('uses the issuer URL for the device flow, the API URL for requests, and persists both', async () => {
    const store = new MemoryConfigStore()
    const server = fakeServer()

    await runLogin(testConfig(store, server))

    const urls = server.requests.map((request) => request.url)
    expect(urls).toContain(`${issuerUrl}/.well-known/oauth-authorization-server`)
    expect(urls).toContain(`${issuerUrl}/api/auth/device/code`)
    const tokenRequest = server.requests.find((request) => request.url === tokenEndpoint)
    expect(tokenRequest?.body).toContain('client_id=omnara-cli')
    const meRequest = server.requests.find((request) => request.url === `${apiUrl}/me`)
    expect(meRequest?.authorization).toBe(`Bearer ${token}`)
    expect(store.file).toEqual({ token, api_url: apiUrl, issuer_url: issuerUrl })
  })

  it('verifies the config can be written before starting the device flow', async () => {
    const server = fakeServer()

    await runLogin(testConfig(new UnwritableConfigStore(), server))

    expect(console.error).toHaveBeenCalledWith('error: config is not writable')
    expect(process.exitCode).toBe(1)
    expect(server.requests).toEqual([])
  })

  it('points an account without organizations to browser onboarding', async () => {
    await runLogin(testConfig(new MemoryConfigStore(), fakeServer()))

    expect(console.log).toHaveBeenCalledWith(
      "This account has no organization yet. Create one at https://self-hosted.example/onboarding, then run 'omnara config select'.",
    )
  })

  it('asks an account with organizations but no default to run config select', async () => {
    const server = fakeServer(() =>
      Promise.resolve(
        currentUser([
          { id: orgId, name: 'Acme', role: 'owner', created_at: '2026-01-01T00:00:00Z' },
        ]),
      ),
    )

    await runLogin(testConfig(new MemoryConfigStore(), server))

    expect(console.log).toHaveBeenCalledWith(
      "Run 'omnara config select' to choose a default organization and project.",
    )
  })

  it('keeps the login successful when account validation fails', async () => {
    const store = new MemoryConfigStore()
    const server = fakeServer(() => Promise.reject(new Error('temporary network failure')))

    await runLogin(testConfig(store, server))

    expect(store.file).toEqual({ token, api_url: apiUrl, issuer_url: issuerUrl })
    expect(console.log).toHaveBeenCalledWith('Logged in')
    expect(console.error).toHaveBeenCalledWith(
      'warning: could not verify the account or saved organization and project defaults: temporary network failure',
    )
  })
})
