import { bearerToken, createOmnaraClient, type OmnaraClient } from '@omnara/sdk'
import type { Command } from 'commander'

import { type ConfigFile, type ConfigStore, fileConfigStore } from './config-file.ts'
import { createLoginReporter, loginWithDevice } from './device-login.ts'
import {
  canPromptInteractively,
  promptOrgSelection,
  promptProjectSelection,
} from './interactive.ts'
import { CliInputError, runCliAction } from './output.ts'
import { sleepSeconds } from './poll.ts'

export const DEFAULT_API_URL = 'https://api.omnara.com/v1'
export const DEFAULT_ISSUER_URL = 'https://app.omnara.com'

export interface CliConfig {
  client: OmnaraClient
  apiUrl: string
  issuerUrl: string
  readonly defaultOrgId?: string
  readonly defaultProjectId?: string
  store: ConfigStore
  fetch: typeof fetch
  sleep: (seconds: number) => Promise<void>
  ensureLoggedIn: () => Promise<void>
}

interface SavedDefaults {
  orgId?: string
  projectId?: string
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
  const store = fileConfigStore()
  const loaded = store.read()
  const migrated = migrateLegacyBaseUrl(loaded)
  const file = migrated === undefined ? loaded : store.update({ ...migrated, base_url: undefined })
  const { apiUrl, issuerUrl } = resolveUrls(file, process.env)
  let saved: SavedDefaults = { orgId: file.org_id, projectId: file.project_id }
  const resolveToken = createTokenResolver({
    savedToken: process.env.OMNARA_API_KEY ?? file.token,
    canPrompt: canPromptInteractively,
    login: async () => {
      const report = createLoginReporter(`Not logged in yet. Log in to ${issuerUrl}`)
      const login = await loginWithDevice({
        apiUrl,
        issuerUrl,
        browser: true,
        report,
        store,
        fetch: globalThis.fetch,
        sleep: sleepSeconds,
      })
      saved = { orgId: login.orgId, projectId: login.projectId }
      report.finish('Continuing')
      return login.token
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
    store,
    fetch: globalThis.fetch,
    sleep: sleepSeconds,
    get defaultOrgId() {
      return process.env.OMNARA_ORG_ID ?? saved.orgId
    },
    get defaultProjectId() {
      return process.env.OMNARA_PROJECT_ID ?? saved.projectId
    },
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

function printConfig(store: ConfigStore): void {
  const file = store.read()
  console.log(`config file  ${store.path}`)
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
          const patch: Partial<ConfigFile> = {}
          if (options.org !== undefined) {
            patch.org_id = options.org
            patch.project_id = undefined
          }
          if (options.project !== undefined) patch.project_id = options.project
          if (options.apiUrl !== undefined) patch.api_url = options.apiUrl
          if (options.issuerUrl !== undefined) patch.issuer_url = options.issuerUrl
          cli.store.update(patch)
        }
        printConfig(cli.store)
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
        const orgId = await promptOrgSelection(cli.client, cli.issuerUrl)
        const projectId = await promptProjectSelection(cli.client, orgId)
        cli.store.update({ org_id: orgId, project_id: projectId })
        printConfig(cli.store)
      })
    })
}
