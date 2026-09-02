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

const zJsonText = z.string().transform((raw, ctx) => {
  try {
    return z.json().parse(JSON.parse(raw))
  } catch (error) {
    ctx.addIssue(error instanceof Error ? error.message : String(error))
    return z.NEVER
  }
})

type ConfigLoad = { readable: true; file: ConfigFile } | { readable: false; reason: string }

function loadConfigFile(): ConfigLoad {
  const path = configFilePath()
  if (!existsSync(path)) return { readable: true, file: {} }
  const json = zJsonText.safeParse(readFileSync(path, 'utf8'))
  if (!json.success) return { readable: false, reason: z.prettifyError(json.error) }
  const file = zConfigFile.safeParse(json.data)
  return file.success
    ? { readable: true, file: file.data }
    : { readable: false, reason: z.prettifyError(file.error) }
}

export function readConfigFile(): ConfigFile {
  const loaded = loadConfigFile()
  if (loaded.readable) return loaded.file
  warnConfigIgnored(`warning: ignoring unreadable config at ${configFilePath()}: ${loaded.reason}`)
  return {}
}

export function readConfigFileForUpdate(): ConfigFile {
  const loaded = loadConfigFile()
  if (loaded.readable) return loaded.file
  throw new CliInputError(
    `refusing to modify unreadable config at ${configFilePath()} (fix or delete it first): ${loaded.reason}`,
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
