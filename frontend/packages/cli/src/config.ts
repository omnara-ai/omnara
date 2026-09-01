import { bearerToken, createOmnaraClient, type OmnaraClient } from '@omnara/sdk'
import type { Command } from 'commander'

import { type ConfigFile, configFilePath, readConfigFile, updateConfigFile } from './config-file.ts'
import { createLoginReporter, loginWithDevice } from './device-login.ts'
import {
  canPromptInteractively,
  promptOrgSelection,
  promptProjectSelection,
} from './interactive.ts'
import { CliInputError, runCliAction } from './output.ts'

export const DEFAULT_API_URL = 'https://api.omnara.com/v1'
export const DEFAULT_ISSUER_URL = 'https://app.omnara.com'

export interface CliConfig {
  client: OmnaraClient
  apiUrl: string
  issuerUrl: string
  defaultOrgId?: string
  defaultProjectId?: string
  ensureLoggedIn: () => Promise<void>
}

export interface ResolvedUrls {
  apiUrl: string
  issuerUrl: string
}

export function resolveUrls(file: ConfigFile, env: NodeJS.ProcessEnv): ResolvedUrls {
  return {
    apiUrl: env.OMNARA_API_URL ?? file.api_url ?? DEFAULT_API_URL,
    issuerUrl: env.OMNARA_ISSUER_URL ?? file.issuer_url ?? DEFAULT_ISSUER_URL,
  }
}

export function migrateLegacyBaseUrl(file: ConfigFile): ConfigFile | undefined {
  const { base_url: baseUrl, ...rest } = file
  if (baseUrl === undefined) return undefined
  return rest
}

export interface TokenResolverOptions {
  savedToken: string | undefined
  canPrompt: () => boolean
  login: () => Promise<string>
}

export function createTokenResolver(options: TokenResolverOptions): () => Promise<string> {
  let token = options.savedToken
  let pending: Promise<string> | undefined
  return () => {
    if (token !== undefined) return Promise.resolve(token)
    if (!options.canPrompt()) {
      return Promise.reject(
        new CliInputError("not logged in: run 'omnara login' or set OMNARA_API_KEY"),
      )
    }
    pending ??= options.login().then((result) => {
      token = result
      return result
    })
    return pending
  }
}

export function loadConfig(): CliConfig {
  const loaded = readConfigFile()
  const migrated = migrateLegacyBaseUrl(loaded)
  const file =
    migrated === undefined ? loaded : updateConfigFile({ ...migrated, base_url: undefined })
  const { apiUrl, issuerUrl } = resolveUrls(file, process.env)
  const resolveToken = createTokenResolver({
    savedToken: process.env.OMNARA_API_KEY ?? file.token,
    canPrompt: canPromptInteractively,
    login: async () => {
      const report = createLoginReporter(`Not logged in yet. Log in to ${issuerUrl}`)
      const { token } = await loginWithDevice({ apiUrl, issuerUrl, browser: true, report })
      report.finish('Continuing')
      return token
    },
  })
  const client = createOmnaraClient({
    baseUrl: apiUrl,
    auth: bearerToken(resolveToken),
    headers: { 'User-Agent': 'omnara-cli' },
  })
  return {
    client,
    apiUrl,
    issuerUrl,
    defaultOrgId: process.env.OMNARA_ORG_ID ?? file.org_id,
    defaultProjectId: process.env.OMNARA_PROJECT_ID ?? file.project_id,
    ensureLoggedIn: async () => {
      await resolveToken()
    },
  }
}

interface ConfigOptions {
  org?: string
  project?: string
  apiUrl?: string
  issuerUrl?: string
}

function describeValue(
  envName: string,
  fileValue: string | undefined,
  defaultValue?: string,
): string {
  const envValue = process.env[envName]
  if (envValue !== undefined) {
    const shadowed = fileValue !== undefined ? ` (config has ${fileValue})` : ''
    return `${envValue}  [from ${envName}${shadowed}]`
  }
  if (fileValue !== undefined) return `${fileValue}  [from config file]`
  if (defaultValue !== undefined) return `${defaultValue}  [default]`
  return '(not set)'
}

function printConfig(): void {
  const file = readConfigFile()
  console.log(`config file  ${configFilePath()}`)
  console.log(`org_id       ${describeValue('OMNARA_ORG_ID', file.org_id)}`)
  console.log(`project_id   ${describeValue('OMNARA_PROJECT_ID', file.project_id)}`)
  const { apiUrl, issuerUrl } = resolveUrls(file, {})
  console.log(`api_url      ${describeValue('OMNARA_API_URL', file.api_url, apiUrl)}`)
  console.log(`issuer_url   ${describeValue('OMNARA_ISSUER_URL', file.issuer_url, issuerUrl)}`)
}

export function registerConfigCommand(program: Command, cli: CliConfig): void {
  const config = program
    .command('config')
    .description('Show or set the default organization and project')
    .option('--org <org-id>', 'save this organization ID as the default')
    .option('--project <project-id>', 'save this project ID as the default')
    .option('--api-url <url>', 'save this API root URL (used for requests) as the default')
    .option(
      '--issuer-url <url>',
      'save this web app origin (used for login and browser links) as the default',
    )
    .action(async (options: ConfigOptions) => {
      await runCliAction(() => {
        if (
          options.org !== undefined ||
          options.project !== undefined ||
          options.apiUrl !== undefined ||
          options.issuerUrl !== undefined
        ) {
          const clearStaleProject = options.org !== undefined && options.project === undefined
          updateConfigFile({
            ...(options.org !== undefined ? { org_id: options.org } : {}),
            ...(clearStaleProject
              ? { project_id: undefined }
              : options.project !== undefined
                ? { project_id: options.project }
                : {}),
            ...(options.apiUrl !== undefined ? { api_url: options.apiUrl } : {}),
            ...(options.issuerUrl !== undefined ? { issuer_url: options.issuerUrl } : {}),
          })
        }
        printConfig()
      })
    })
  config
    .command('select')
    .description('Interactively choose the default organization and project')
    .action(async () => {
      await runCliAction(async () => {
        if (!canPromptInteractively()) {
          throw new CliInputError('interactive selection needs a terminal')
        }
        await cli.ensureLoggedIn()
        const orgId = await promptOrgSelection(cli.client)
        const projectId = await promptProjectSelection(cli.client, orgId)
        updateConfigFile({ org_id: orgId, project_id: projectId })
        printConfig()
      })
    })
}
