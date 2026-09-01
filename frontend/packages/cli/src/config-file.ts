import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { dirname, join } from 'node:path'

import { zOrganizationId, zProjectId } from '@omnara/sdk/zod'
import * as z from 'zod'

import { CliInputError } from './output.ts'

const zConfigFile = z.looseObject({
  api_url: z.url().optional(),
  issuer_url: z.url().optional(),
  base_url: z.url().optional(),
  token: z.string().min(1).optional(),
  org_id: zOrganizationId.optional(),
  project_id: zProjectId.optional(),
})

export type ConfigFile = z.infer<typeof zConfigFile>

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
