import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import type { AgentProfileSource } from './bootstrap.js'
import type { CliEnv } from './env.js'

export interface CliAgentRecord {
  id: string
  profileName: string
  createdAt: string
  cwd: string
}

export interface CliState {
  apiUrl: string
  orgId?: string
  model?: string
  effort?: string
  permissions?: Record<string, string>
  display?: 'simple' | 'default' | 'full'
  agents?: CliAgentRecord[]
}

function stateFile(env: CliEnv): string {
  return path.join(env.daemonHome, 'cli-state.json')
}

export function loadCliState(env: CliEnv): CliState {
  try {
    const state = JSON.parse(readFileSync(stateFile(env), 'utf8')) as CliState
    if (state.apiUrl !== env.apiUrl) return { apiUrl: env.apiUrl }
    return state
  } catch {
    return { apiUrl: env.apiUrl }
  }
}

export function updateCliState(env: CliEnv, patch: Partial<CliState>): CliState {
  const state = { ...loadCliState(env), ...patch, apiUrl: env.apiUrl }
  mkdirSync(env.daemonHome, { recursive: true })
  writeFileSync(stateFile(env), `${JSON.stringify(state, null, 2)}\n`, { mode: 0o600 })
  return state
}

export function recordAgent(env: CliEnv, record: CliAgentRecord): void {
  const state = loadCliState(env)
  const agents = [record, ...(state.agents ?? []).filter((agent) => agent.id !== record.id)].slice(0, 20)
  updateCliState(env, { agents })
}

export function applyStoredOverrides(profile: AgentProfileSource, state: CliState): void {
  if (state.model != null) {
    profile.model = { ...profile.model, name: state.model }
  }
  if (state.effort != null) {
    profile.model = { ...profile.model, reasoning: { effort: state.effort } }
  }
  const tools = profile.tools
  if (tools == null) return
  for (const [tool, mode] of Object.entries(state.permissions ?? {})) {
    if (tools[tool] != null) tools[tool] = { ...tools[tool], permission: { mode } }
  }
}
