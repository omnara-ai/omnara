import { existsSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

export const exampleDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

export interface CliEnv {
  apiUrl: string
  apiKey: string
  orgId: string | undefined
  projectName: string
  profilePath: string
  repoUri: string | undefined
  repoCred: string | undefined
  omnaradBinary: string | undefined
  daemonHome: string
}

function expandHome(value: string): string {
  if (value === '~') return os.homedir()
  if (value.startsWith('~/')) return path.join(os.homedir(), value.slice(2))
  return value
}

export function loadEnv(): CliEnv {
  const envFile = path.join(exampleDir, '.env')
  if (existsSync(envFile)) process.loadEnvFile(envFile)

  const apiKey = process.env.OMNARA_API_KEY?.trim() ?? ''
  if (apiKey === '') {
    throw new Error('OMNARA_API_KEY is required; copy .env.example to .env and set a personal access token')
  }
  const orgId = process.env.OMNARA_ORG_ID?.trim() || undefined
  const apiUrl = (process.env.OMNARA_API_URL?.trim() || 'http://localhost:8080').replace(/\/+$/, '')
  const omnaradBinary = process.env.OMNARAD_BINARY?.trim() || undefined
  const daemonHome = expandHome(process.env.OMNARA_DAEMON_HOME?.trim() || path.join(os.homedir(), '.omnara-cli-agent'))
  return {
    apiUrl,
    apiKey,
    orgId,
    projectName: process.env.OMNARA_PROJECT_NAME?.trim() || 'cli-agent',
    profilePath: path.join(exampleDir, 'agent-profile.yaml'),
    repoUri: process.env.REPO_URI?.trim() || undefined,
    repoCred: process.env.REPO_CRED?.trim() || undefined,
    omnaradBinary: omnaradBinary != null ? expandHome(omnaradBinary) : undefined,
    daemonHome,
  }
}
