import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { dirname, join } from 'node:path'

import { bearerToken, createOmnaraClient, type OmnaraClient } from '@omnara/sdk'
import { zOrganizationId, zProjectId } from '@omnara/sdk/zod'
import type { Command } from 'commander'
import * as z from 'zod'

import {
  canPromptInteractively,
  promptOrgSelection,
  promptProjectSelection,
} from './interactive.ts'
import { CliInputError, runCliAction } from './output.ts'

export const DEFAULT_API_URL = 'https://api.omnara.com/v1'
export const DEFAULT_ISSUER_URL = 'https://app.omnara.com'

const zConfigFile = z.looseObject({
  api_url: z.url().optional(),
  issuer_url: z.url().optional(),
  base_url: z.url().optional(),
  token: z.string().min(1).optional(),
  org_id: zOrganizationId.optional(),
  project_id: zProjectId.optional(),
})

export type ConfigFile = z.infer<typeof zConfigFile>

export interface CliConfig {
  client: OmnaraClient
  apiUrl: string
  issuerUrl: string
  defaultOrgId?: string
  defaultProjectId?: string
}

export function configFilePath(): string {
  return join(homedir(), '.config', 'omnara', 'config.json')
}

let warnedAboutConfig = false

function warnConfigIgnored(message: string): void {
  if (warnedAboutConfig) return
  warnedAboutConfig = true
  console.error(message)
}

const zConfigContents = z
  .string()
  .transform((raw, ctx): unknown => {
    try {
      return JSON.parse(raw)
    } catch (error) {
      ctx.addIssue(error instanceof Error ? error.message : String(error))
      return z.NEVER
    }
  })
  .pipe(zConfigFile)

function loadConfigFile(): z.ZodSafeParseResult<ConfigFile> {
  const path = configFilePath()
  if (!existsSync(path)) return { success: true, data: {} }
  return zConfigContents.safeParse(readFileSync(path, 'utf8'))
}

export function readConfigFile(): ConfigFile {
  const result = loadConfigFile()
  if (result.success) return result.data
  warnConfigIgnored(
    `warning: ignoring unreadable config at ${configFilePath()}: ${z.prettifyError(result.error)}`,
  )
  return {}
}

export function readConfigFileForUpdate(): ConfigFile {
  const result = loadConfigFile()
  if (result.success) return result.data
  throw new CliInputError(
    `refusing to modify unreadable config at ${configFilePath()} (fix or delete it first): ${z.prettifyError(result.error)}`,
  )
}

export function updateConfigFile(patch: Partial<ConfigFile>): ConfigFile {
  const merged = Object.fromEntries(
    Object.entries({ ...readConfigFileForUpdate(), ...patch }).filter(
      ([, value]) => value !== undefined,
    ),
  )
  const result = zConfigFile.safeParse(merged)
  if (!result.success) {
    throw new CliInputError(`refusing to save invalid config:\n${z.prettifyError(result.error)}`)
  }
  mkdirSync(dirname(configFilePath()), { recursive: true, mode: 0o700 })
  writeFileSync(configFilePath(), `${JSON.stringify(result.data, null, 2)}\n`, { mode: 0o600 })
  chmodSync(configFilePath(), 0o600)
  return result.data
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

export function loadConfig(): CliConfig {
  const loaded = readConfigFile()
  const migrated = migrateLegacyBaseUrl(loaded)
  const file =
    migrated === undefined ? loaded : updateConfigFile({ ...migrated, base_url: undefined })
  const { apiUrl, issuerUrl } = resolveUrls(file, process.env)
  const token = process.env.OMNARA_API_KEY ?? file.token
  const client = createOmnaraClient({
    baseUrl: apiUrl,
    auth: token ? bearerToken(token) : undefined,
    headers: { 'User-Agent': 'omnara-cli' },
  })
  return {
    client,
    apiUrl,
    issuerUrl,
    defaultOrgId: process.env.OMNARA_ORG_ID ?? file.org_id,
    defaultProjectId: process.env.OMNARA_PROJECT_ID ?? file.project_id,
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
      'save this web app origin (used for login, browser links, and the installer) as the default',
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
        const orgId = await promptOrgSelection(cli.client)
        const projectId = await promptProjectSelection(cli.client, orgId)
        updateConfigFile({ org_id: orgId, project_id: projectId })
        printConfig()
      })
    })
}
