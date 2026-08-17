import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { dirname, join } from 'node:path'
import type { Command } from 'commander'
import * as z from 'zod'
import { bearerToken, createOmnaraClient, type OmnaraClient } from '@omnara/sdk'
import { zOrganizationId, zProjectId } from '@omnara/sdk/zod'
import { CliInputError, runCliAction } from './output.ts'
import {
  canPromptInteractively,
  promptOrgSelection,
  promptProjectSelection,
} from './interactive.ts'

export const DEFAULT_BASE_URL = 'https://app.omnara.com'

const zConfigFile = z.object({
  base_url: z.url().optional(),
  token: z.string().min(1).optional(),
  org_id: zOrganizationId.optional(),
  project_id: zProjectId.optional(),
})

export type ConfigFile = z.infer<typeof zConfigFile>

export interface CliContext {
  client: OmnaraClient
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

export function readConfigFile(): ConfigFile {
  let raw: string
  try {
    raw = readFileSync(configFilePath(), 'utf8')
  } catch {
    return {}
  }
  let contents: ConfigFile
  try {
    contents = JSON.parse(raw) as ConfigFile
  } catch (error) {
    const reason = error instanceof Error ? error.message : String(error)
    warnConfigIgnored(`warning: ignoring unreadable config at ${configFilePath()}: ${reason}`)
    return {}
  }
  const result = zConfigFile.safeParse(contents)
  if (!result.success) {
    warnConfigIgnored(
      `warning: ignoring invalid config at ${configFilePath()}:\n${z.prettifyError(result.error)}`,
    )
    return {}
  }
  return result.data
}

export function updateConfigFile(patch: Partial<ConfigFile>): ConfigFile {
  return writeConfig({ ...readConfigFile(), ...patch })
}

export function removeConfigKeys(keys: readonly (keyof ConfigFile)[]): ConfigFile {
  const config = readConfigFile()
  for (const key of keys) delete config[key]
  return writeConfig(config)
}

function writeConfig(config: ConfigFile): ConfigFile {
  const result = zConfigFile.safeParse(config)
  if (!result.success) {
    throw new CliInputError(`refusing to save invalid config:\n${z.prettifyError(result.error)}`)
  }
  mkdirSync(dirname(configFilePath()), { recursive: true })
  writeFileSync(configFilePath(), `${JSON.stringify(result.data, null, 2)}\n`)
  return result.data
}

export function loadContext(): CliContext {
  const file = readConfigFile()
  const baseUrl = process.env.OMNARA_BASE_URL ?? file.base_url ?? DEFAULT_BASE_URL
  const token = process.env.OMNARA_API_KEY ?? file.token
  const client = createOmnaraClient({
    baseUrl,
    auth: token ? bearerToken(token) : undefined,
  })
  return {
    client,
    defaultOrgId: process.env.OMNARA_ORG_ID ?? file.org_id,
    defaultProjectId: process.env.OMNARA_PROJECT_ID ?? file.project_id,
  }
}

interface ContextOptions {
  org?: string
  project?: string
  baseUrl?: string
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

function printContext(): void {
  const file = readConfigFile()
  console.log(`config file  ${configFilePath()}`)
  console.log(`org_id       ${describeValue('OMNARA_ORG_ID', file.org_id)}`)
  console.log(`project_id   ${describeValue('OMNARA_PROJECT_ID', file.project_id)}`)
  console.log(`base_url     ${describeValue('OMNARA_BASE_URL', file.base_url, DEFAULT_BASE_URL)}`)
}

export function registerContextCommand(program: Command, ctx: CliContext): void {
  const context = program
    .command('context')
    .description('Show or set the default organization and project')
    .option('--org <org-id>', 'save this organization ID as the default')
    .option('--project <project-id>', 'save this project ID as the default')
    .option('--base-url <url>', 'save this API base URL as the default')
    .action(async (_options: ContextOptions, command: Command) => {
      await runCliAction(async () => {
        const options = command.optsWithGlobals<ContextOptions>()
        if (
          options.org !== undefined ||
          options.project !== undefined ||
          options.baseUrl !== undefined
        ) {
          updateConfigFile({
            ...(options.org !== undefined ? { org_id: options.org } : {}),
            ...(options.project !== undefined ? { project_id: options.project } : {}),
            ...(options.baseUrl !== undefined ? { base_url: options.baseUrl } : {}),
          })
        }
        printContext()
      })
    })
  context
    .command('select')
    .description('Interactively choose the default organization and project')
    .action(async () => {
      await runCliAction(async () => {
        if (!canPromptInteractively()) {
          throw new CliInputError('interactive selection needs a terminal')
        }
        removeConfigKeys(['org_id', 'project_id'])
        const orgId = await promptOrgSelection(ctx.client)
        updateConfigFile({ org_id: orgId })
        const projectId = await promptProjectSelection(ctx.client, orgId)
        updateConfigFile({ project_id: projectId })
        printContext()
      })
    })
}
