import { existsSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

export const exampleDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
export const repoRoot = path.resolve(exampleDir, '..', '..')

export interface CliEnv {
  apiUrl: string
  apiKey: string
  apiKeyKind: 'pat' | 'org'
  orgId: string | undefined
  projectName: string
  profilePath: string
  omnaradBinary: string | undefined
  daemonHome: string
}

function expandHome(value: string): string {
  if (value === '~' || value.startsWith('~/')) {
    return path.join(os.homedir(), value.slice(1))
  }
  return value
}

export function loadEnv(): CliEnv {
  const envFile = path.join(exampleDir, '.env')
  if (existsSync(envFile)) process.loadEnvFile(envFile)

  const apiKey = process.env.OMNARA_API_KEY?.trim() ?? ''
  if (apiKey === '') {
    throw new Error('OMNARA_API_KEY is required; copy .env.example to .env and set a personal access token')
  }
  let apiKeyKind: 'pat' | 'org'
  if (apiKey.startsWith('omnara_pat_')) {
    apiKeyKind = 'pat'
  } else if (apiKey.startsWith('omnara_org_')) {
    apiKeyKind = 'org'
  } else {
    throw new Error(
      'OMNARA_API_KEY must be a personal access token ("omnara_pat_...") or an org API key ("omnara_org_...")',
    )
  }
  const orgId = process.env.OMNARA_ORG_ID?.trim() || undefined
  if (apiKeyKind === 'org' && orgId == null) {
    throw new Error('OMNARA_ORG_ID is required with an org API key; the key is bound to a single organization')
  }
  const apiUrl = (process.env.OMNARA_API_URL?.trim() || 'http://localhost:8080').replace(/\/+$/, '')
  const omnaradBinary = process.env.OMNARAD_BINARY?.trim() || undefined
  const daemonHome = expandHome(process.env.OMNARA_DAEMON_HOME?.trim() || path.join(os.homedir(), '.omnara-cli-agent'))
  return {
    apiUrl,
    apiKey,
    apiKeyKind,
    orgId,
    projectName: process.env.OMNARA_PROJECT_NAME?.trim() || 'cli-agent',
    profilePath: path.join(exampleDir, 'agent-profile.yaml'),
    omnaradBinary: omnaradBinary != null ? expandHome(omnaradBinary) : undefined,
    daemonHome,
  }
}
