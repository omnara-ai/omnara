import type * as sdkModule from '@omnara/sdk'
import { Command } from 'commander'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { CliConfig } from './config.ts'
import { loginTokenName, registerLoginCommand } from './login.ts'

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
  readConfigFileForUpdate: vi.fn(() => ({})),
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

vi.mock('./config.ts', () => ({
  configFilePath: () => '/tmp/omnara-config.json',
  readConfigFile: mocks.readConfigFile,
  readConfigFileForUpdate: mocks.readConfigFileForUpdate,
  updateConfigFile: mocks.updateConfigFile,
}))

vi.mock('./interactive.ts', () => ({
  canPromptInteractively: () => false,
}))

vi.mock('./output.ts', () => ({
  CliInputError: class CliInputError extends Error {},
  runCliAction: (action: () => void | Promise<void>) => Promise.resolve(action()),
}))

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

describe('login', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'log').mockImplementation(() => undefined)
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('uses and persists the configured base URL', async () => {
    const baseUrl = 'https://self-hosted.example'
    const program = new Command()
    const cli: CliConfig = {
      client: {} as CliConfig['client'],
      baseUrl,
    }
    registerLoginCommand(program, cli)

    await program.parseAsync(['node', 'omnara', 'login', '--no-browser'])

    expect(mocks.startDeviceAuth).toHaveBeenCalledWith(
      expect.objectContaining({ issuerUrl: baseUrl, clientId: 'omnara-cli' }),
    )
    expect(mocks.pollDeviceAuthToken).toHaveBeenCalledWith(
      expect.objectContaining({
        tokenEndpoint: 'https://self-hosted.example/api/auth/device/token',
        clientId: 'omnara-cli',
      }),
    )
    expect(mocks.createOmnaraClient).toHaveBeenCalledWith(expect.objectContaining({ baseUrl }))
    expect(mocks.updateConfigFile).toHaveBeenCalledWith({
      token: 'omnara_pat_v1_test',
      base_url: baseUrl,
    })
  })

  it('validates the config before starting the device flow', async () => {
    const program = new Command()
    const cli: CliConfig = {
      client: {} as CliConfig['client'],
      baseUrl: 'https://self-hosted.example',
    }
    mocks.readConfigFileForUpdate.mockImplementationOnce(() => {
      throw new Error('invalid config')
    })
    registerLoginCommand(program, cli)

    await expect(program.parseAsync(['node', 'omnara', 'login', '--no-browser'])).rejects.toThrow(
      'invalid config',
    )

    expect(mocks.startDeviceAuth).not.toHaveBeenCalled()
    expect(mocks.pollDeviceAuthToken).not.toHaveBeenCalled()
  })

  it('keeps the login successful when account validation fails', async () => {
    const baseUrl = 'https://self-hosted.example'
    const program = new Command()
    const cli: CliConfig = {
      client: {} as CliConfig['client'],
      baseUrl,
    }
    mocks.getCurrentUser.mockRejectedValueOnce(new Error('temporary network failure'))
    registerLoginCommand(program, cli)

    await program.parseAsync(['node', 'omnara', 'login', '--no-browser'])

    expect(mocks.updateConfigFile).toHaveBeenCalledWith({
      token: 'omnara_pat_v1_test',
      base_url: baseUrl,
    })
    expect(console.log).toHaveBeenCalledWith('Logged in')
    expect(console.error).toHaveBeenCalledWith(
      'warning: could not verify the account or saved organization and project defaults: temporary network failure',
    )
  })
})
