import type * as sdkModule from '@omnara/sdk'
import { Command } from 'commander'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { CliConfig } from './config.ts'
import { loginTokenName, loginWithDevice } from './device-login.ts'
import { registerLoginCommand } from './login.ts'

const mocks = vi.hoisted(() => ({
  bearerToken: vi.fn(() => ({ authenticate: vi.fn() })),
  createOmnaraClient: vi.fn(() => ({ authenticated: true })),
  getCurrentUser: vi.fn(() =>
    Promise.resolve({
      data: {
        user: { email: 'person@example.com', display_name: 'Person' },
        orgs: [],
      },
    }),
  ),
  pollDeviceAuthToken: vi.fn(() => Promise.resolve('omnara_pat_v1_test')),
  readConfigFile: vi.fn(() => ({})),
  startDeviceAuth: vi.fn(() =>
    Promise.resolve({
      deviceCode: 'device-code',
      userCode: 'ABCDE-FGHIJ',
      verificationUri: 'https://self-hosted.example/device',
      verificationUriComplete: 'https://self-hosted.example/device?user_code=ABCDE-FGHIJ',
      expiresInSeconds: 900,
      intervalSeconds: 5,
      tokenEndpoint: 'https://self-hosted.example/api/auth/device/token',
      clientId: 'omnara-cli',
    }),
  ),
  updateConfigFile: vi.fn((patch: Record<string, unknown>) => patch),
}))

vi.mock('@omnara/sdk', async (importOriginal) => ({
  cliLoginTokenName: (await importOriginal<typeof sdkModule>()).cliLoginTokenName,
  OMNARA_CLI_OAUTH_CLIENT_ID: 'omnara-cli',
  bearerToken: mocks.bearerToken,
  createOmnaraClient: mocks.createOmnaraClient,
  pollDeviceAuthToken: mocks.pollDeviceAuthToken,
  sdk: {
    getCurrentUser: mocks.getCurrentUser,
    listVisibleProjects: vi.fn(),
  },
  startDeviceAuth: mocks.startDeviceAuth,
}))

vi.mock('./config-file.ts', () => ({
  configFilePath: () => '/tmp/omnara-config.json',
  readConfigFile: mocks.readConfigFile,
  updateConfigFile: mocks.updateConfigFile,
}))

vi.mock('./interactive.ts', () => ({
  canPromptInteractively: () => false,
}))

vi.mock('./output.ts', () => ({
  CliInputError: class CliInputError extends Error {},
  runCliAction: (action: () => void | Promise<void>) => Promise.resolve(action()),
}))

function testConfig(apiUrl: string, issuerUrl: string): CliConfig {
  return {
    client: {} as CliConfig['client'],
    apiUrl,
    issuerUrl,
    ensureLoggedIn: () => Promise.resolve(),
  }
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
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'log').mockImplementation(() => undefined)
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
  })

  afterEach(() => {
    mocks.updateConfigFile.mockImplementation((patch: Record<string, unknown>) => patch)
    vi.restoreAllMocks()
  })

  it('drops saved defaults the new account cannot access', async () => {
    mocks.updateConfigFile.mockImplementation((patch: Record<string, unknown>) => ({
      org_id: 'org_00000000000000000000000001',
      project_id: 'proj_0000000000000000000000001',
      ...patch,
    }))

    const result = await loginWithDevice({
      apiUrl: 'https://self-hosted.example/api/v1',
      issuerUrl: 'https://self-hosted.example',
      browser: false,
      report: {
        showCode: () => undefined,
        startWaiting: () => undefined,
        stopWaiting: () => undefined,
        success: () => undefined,
        info: () => undefined,
        warn: () => undefined,
        finish: () => undefined,
      },
    })

    expect(result).toEqual({ token: 'omnara_pat_v1_test', orgId: undefined, projectId: undefined })
    expect(mocks.updateConfigFile).toHaveBeenCalledWith({
      org_id: undefined,
      project_id: undefined,
    })
  })
})

describe('login', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'log').mockImplementation(() => undefined)
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('uses the issuer URL for the device flow, the API URL for requests, and persists both', async () => {
    const apiUrl = 'https://self-hosted.example/api/v1'
    const issuerUrl = 'https://self-hosted.example'
    const program = new Command()
    const cli = testConfig(apiUrl, issuerUrl)
    registerLoginCommand(program, cli)

    await program.parseAsync(['node', 'omnara', 'login', '--no-browser'])

    expect(mocks.startDeviceAuth).toHaveBeenCalledWith(
      expect.objectContaining({ issuerUrl, clientId: 'omnara-cli' }),
    )
    expect(mocks.pollDeviceAuthToken).toHaveBeenCalledWith(
      expect.objectContaining({
        tokenEndpoint: 'https://self-hosted.example/api/auth/device/token',
        clientId: 'omnara-cli',
      }),
    )
    expect(mocks.createOmnaraClient).toHaveBeenCalledWith(
      expect.objectContaining({ baseUrl: apiUrl }),
    )
    expect(mocks.updateConfigFile).toHaveBeenCalledWith({
      token: 'omnara_pat_v1_test',
      api_url: apiUrl,
      issuer_url: issuerUrl,
    })
  })

  it('verifies the config can be written before starting the device flow', async () => {
    const program = new Command()
    const cli = testConfig('https://self-hosted.example/api/v1', 'https://self-hosted.example')
    mocks.updateConfigFile.mockImplementationOnce(() => {
      throw new Error('config is not writable')
    })
    registerLoginCommand(program, cli)

    await expect(program.parseAsync(['node', 'omnara', 'login', '--no-browser'])).rejects.toThrow(
      'config is not writable',
    )

    expect(mocks.startDeviceAuth).not.toHaveBeenCalled()
    expect(mocks.pollDeviceAuthToken).not.toHaveBeenCalled()
  })

  it('keeps the login successful when account validation fails', async () => {
    const apiUrl = 'https://self-hosted.example/api/v1'
    const issuerUrl = 'https://self-hosted.example'
    const program = new Command()
    const cli = testConfig(apiUrl, issuerUrl)
    mocks.getCurrentUser.mockRejectedValueOnce(new Error('temporary network failure'))
    registerLoginCommand(program, cli)

    await program.parseAsync(['node', 'omnara', 'login', '--no-browser'])

    expect(mocks.updateConfigFile).toHaveBeenCalledWith({
      token: 'omnara_pat_v1_test',
      api_url: apiUrl,
      issuer_url: issuerUrl,
    })
    expect(console.log).toHaveBeenCalledWith('Logged in')
    expect(console.error).toHaveBeenCalledWith(
      'warning: could not verify the account or saved organization and project defaults: temporary network failure',
    )
  })
})
