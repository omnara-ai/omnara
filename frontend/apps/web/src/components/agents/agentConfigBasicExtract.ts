import type { ToolPermissionSelection } from '@omnara/sdk'

import type {
  BasicConfig,
  BasicMachineSource,
  BasicMcpServer,
} from '@/components/agents/agentConfigBasic'
import type { BasicTool } from '@/components/agents/AgentConfigToolsField'
import {
  emptyProviderOptions,
  type EnvOverlayRow,
  type ProviderOptionsDraft,
  type SecretEnvOverlayRow,
} from '@/components/machines/machineOverrides'
import { machinePoolProviderDefinitions } from '@/components/org/machinePoolProviders'

const machineEntryKeys = new Set(['machine_name', 'cwd', 'env_overlay', 'secret_env_overlay'])
const poolEntryKeys = new Set([
  'machine_pool_name',
  'initial_num_machines',
  'max_machines',
  'machine_cpu',
  'machine_memory_mb',
  'machine_provider_options_overlay',
  'cwd',
  'env_overlay',
  'secret_env_overlay',
])
const toolEntryKeys = new Set(['type', 'enabled', 'permission'])
const permissionKeys = new Set(['mode', 'parameters'])
const mcpEntryKeys = new Set(['url', 'permission', 'default_enabled', 'auth'])

function hasOnlyKeys(record: Record<string, unknown>, allowed: ReadonlySet<string>) {
  return Object.keys(record).every((key) => allowed.has(key))
}

export function extractBasicConfig(doc: Record<string, unknown>): BasicConfig | null {
  if (doc.version !== undefined && doc.version !== 'v1') return null
  if (typeof doc.instruction !== 'string' || !isRecord(doc.model)) return null
  const { provider_config: providerConfig, name: modelName } = doc.model
  if (typeof providerConfig !== 'string' || typeof modelName !== 'string') return null

  const machineSources = extractList(doc.machine_sources, extractMachineSource)
  const tools = extractMap(doc.tools, extractTool)
  const skillIds = extractList(doc.skills, (entry) => (typeof entry === 'string' ? entry : null))
  const mcpServers = extractMap(doc.mcp, extractMcpServer)
  if (machineSources == null || tools == null || skillIds == null || mcpServers == null) {
    return null
  }

  return {
    // Block scalars parse back with a trailing newline the builder's textarea
    // wouldn't show; trim here so the seeded instruction matches what was
    // typed and an untouched draft compares equal to the source.
    instruction: normalizeMultiline(doc.instruction),
    providerConfig,
    modelName,
    machineSources,
    tools,
    mcpServers,
    skillIds,
  }
}

export function normalizeMultiline(value: string) {
  return value.replace(/\r\n?/g, '\n').trimEnd()
}

// Depth cap so alias cycles inside permission parameters can't recurse
// forever; past it the entry just counts as changed and gets rewritten.
export function deepEqual(a: unknown, b: unknown, depth = 0): boolean {
  if (Object.is(a, b)) return true
  if (depth > 64) return false
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false
    return a.every((item, index) => deepEqual(item, b[index], depth + 1))
  }
  if (!isRecord(a) || !isRecord(b)) return false
  const keys = Object.keys(a)
  if (keys.length !== Object.keys(b).length) return false
  return keys.every((key) => key in b && deepEqual(a[key], b[key], depth + 1))
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function extractList<T>(value: unknown, extract: (entry: unknown) => T | null): T[] | null {
  if (value === undefined) return []
  if (!Array.isArray(value)) return null
  const items: T[] = []
  for (const entry of value) {
    const item = extract(entry)
    if (item == null) return null
    items.push(item)
  }
  return items
}

function extractMap<T>(
  value: unknown,
  extract: (name: string, entry: unknown) => T | null,
): T[] | null {
  if (value === undefined) return []
  if (!isRecord(value)) return null
  const items: T[] = []
  for (const [name, entry] of Object.entries(value)) {
    const item = extract(name, entry)
    if (item == null) return null
    items.push(item)
  }
  return items
}

function extractMachineSource(entry: unknown): BasicMachineSource | null {
  if (!isRecord(entry)) return null
  const isPool = typeof entry.machine_pool_name === 'string'
  if (!hasOnlyKeys(entry, isPool ? poolEntryKeys : machineEntryKeys)) return null
  const name = isPool ? entry.machine_pool_name : entry.machine_name
  if (typeof name !== 'string') return null
  const cwd = entry.cwd ?? ''
  if (typeof cwd !== 'string') return null
  const initialNumMachines = extractCountDraft(entry.initial_num_machines)
  const maxMachines = extractCountDraft(entry.max_machines)
  const machineCpu = extractCountDraft(entry.machine_cpu)
  const machineMemoryMb = extractCountDraft(entry.machine_memory_mb)
  const envRows = extractOverlayRows(entry.env_overlay, (key, value): EnvOverlayRow => {
    return { id: crypto.randomUUID(), key, value }
  })
  const secretEnvRows = extractOverlayRows(
    entry.secret_env_overlay,
    (key, secretId): SecretEnvOverlayRow => ({ id: crypto.randomUUID(), key, secretId }),
  )
  const providerOverlay = isPool
    ? extractProviderOverlay(entry.machine_provider_options_overlay)
    : { provider: '', options: emptyProviderOptions }
  if (
    initialNumMachines == null ||
    maxMachines == null ||
    machineCpu == null ||
    machineMemoryMb == null ||
    envRows == null ||
    secretEnvRows == null ||
    providerOverlay == null
  ) {
    return null
  }
  return {
    id: crypto.randomUUID(),
    kind: isPool ? 'pool' : 'machine',
    name,
    provider: providerOverlay.provider,
    managementKind: '',
    defaultCwd: cwd,
    initialNumMachines,
    maxMachines,
    machineCpu,
    machineMemoryMb,
    providerOptions: providerOverlay.options,
    envRows,
    secretEnvRows,
  }
}

/**
 * The overlay doesn't record which provider wrote it, so infer one whose
 * option keys account for every entry. Providers sharing a key read that
 * entry identically, so a wrong pick still seeds the same option values; once
 * the granted pool resolves, the sources field replaces the guess with the
 * pool's real provider, and untouched rows keep their stored overlay either
 * way.
 */
function extractProviderOverlay(
  value: unknown,
): { provider: string; options: ProviderOptionsDraft } | null {
  if (value === undefined) return { provider: '', options: emptyProviderOptions }
  if (!isRecord(value)) return null
  for (const [provider, definition] of Object.entries(machinePoolProviderDefinitions)) {
    const options = { ...emptyProviderOptions }
    let matched = true
    for (const [key, entry] of Object.entries(value)) {
      if (typeof entry !== 'string') return null
      if (key === definition.resource.key) options.resource = entry
      else if (key === definition.location.key) options.location = entry
      else if (key === 'startup_script') options.startupScript = entry
      else matched = false
      if (!matched) break
    }
    if (matched && Object.keys(value).length > 0) return { provider, options }
  }
  return null
}

function extractCountDraft(value: unknown): string | null {
  if (value === undefined) return ''
  if (typeof value === 'number' && Number.isInteger(value) && value > 0) return String(value)
  return null
}

function extractOverlayRows<T>(
  value: unknown,
  makeRow: (key: string, value: string) => T,
): T[] | null {
  if (value === undefined) return []
  if (!isRecord(value)) return null
  const rows: T[] = []
  for (const [key, entry] of Object.entries(value)) {
    if (typeof entry !== 'string') return null
    rows.push(makeRow(key, entry))
  }
  return rows
}

function extractTool(name: string, entry: unknown): BasicTool | null {
  if (!isRecord(entry) || !hasOnlyKeys(entry, toolEntryKeys)) return null
  if (entry.type !== undefined && entry.type !== 'built_in') return null
  if (entry.enabled !== undefined && entry.enabled !== null && entry.enabled !== true) return null
  const permission = extractPermission(entry.permission)
  return permission == null ? null : { name, permission }
}

function extractPermission(value: unknown): ToolPermissionSelection | null {
  if (!isRecord(value) || !hasOnlyKeys(value, permissionKeys)) return null
  if (typeof value.mode !== 'string') return null
  if (value.parameters === undefined) return { mode: value.mode, parameters: {} }
  return isRecord(value.parameters) ? { mode: value.mode, parameters: value.parameters } : null
}

function extractMcpServer(name: string, entry: unknown): BasicMcpServer | null {
  if (!isRecord(entry) || !hasOnlyKeys(entry, mcpEntryKeys)) return null
  const permission = extractPermission(entry.permission)
  if (typeof entry.url !== 'string' || permission == null) return null
  const defaultEnabled = entry.default_enabled ?? true
  if (typeof defaultEnabled !== 'boolean') return null
  const server: BasicMcpServer = {
    id: crypto.randomUUID(),
    name,
    url: entry.url,
    permission,
    defaultEnabled,
    authType: 'none',
    secretId: '',
    service: '',
    region: '',
  }
  if (entry.auth === undefined) return server
  if (!isRecord(entry.auth)) return null
  const { type, secret_id: secretId, service, region } = entry.auth
  if (type !== 'oauth' && type !== 'bearer' && type !== 'sigv4') return null
  if (typeof secretId !== 'string') return null
  if (type !== 'sigv4') {
    if (!hasOnlyKeys(entry.auth, new Set(['type', 'secret_id']))) return null
    return { ...server, authType: type, secretId }
  }
  if (!hasOnlyKeys(entry.auth, new Set(['type', 'secret_id', 'service', 'region']))) return null
  if (typeof service !== 'string' || typeof region !== 'string') return null
  return { ...server, authType: type, secretId, service, region }
}
