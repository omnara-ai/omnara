import type { ToolPermissionSelection } from '@omnara/sdk'
import { parse } from 'yaml'

import {
  type BasicConfig,
  type BasicMachineSource,
  type BasicMcpServer,
  serializeBasicConfig,
} from '@/components/agents/agentConfigBasicSerialization'
import type { BasicTool } from '@/components/agents/AgentConfigToolsField'
import {
  emptyProviderOptions,
  type EnvOverlayRow,
  type ProviderOptionsDraft,
  type SecretEnvOverlayRow,
} from '@/components/machines/machineOverrides'
import { machinePoolProviderDefinitions } from '@/components/org/machinePoolProviders'

/**
 * Best-effort inverse of serializeBasicConfig. Returns a builder draft only
 * when re-serializing it yields an equivalent parsed document, so the builder
 * can never silently change or drop configuration data it doesn't understand
 * (unknown fields, provider options overlays, non-built-in tools). Formatting
 * and comments are not preserved: saving from the builder normalizes them.
 */
export function deserializeBasicConfig(source: string): BasicConfig | null {
  let doc: unknown
  try {
    doc = parse(source)
  } catch {
    return null
  }
  if (!isRecord(doc)) return null
  const config = extractBasicConfig(doc)
  if (config == null) return null
  const emitted: unknown = parse(serializeBasicConfig(config))
  return deepEqual(normalizeDocument(emitted), normalizeDocument(doc)) ? config : null
}

/**
 * Differences the serializer is allowed to introduce against the source:
 * normalized formatting plus spelling out the compiler's defaults (tool
 * `type: built_in`, tool `enabled: true`, mcp `default_enabled: true`).
 */
function normalizeDocument(doc: unknown): unknown {
  if (!isRecord(doc)) return doc
  const normalized = structuredClone(doc)
  if (normalized.version === 'v1') delete normalized.version
  if (typeof normalized.instruction === 'string') {
    normalized.instruction = normalized.instruction.replace(/\r\n?/g, '\n').trimEnd()
  }
  if (isRecord(normalized.tools)) {
    for (const entry of Object.values(normalized.tools)) {
      if (!isRecord(entry)) continue
      entry.type ??= 'built_in'
      if (entry.enabled === true || entry.enabled === null) delete entry.enabled
      dropEmptyParameters(entry)
    }
  }
  if (isRecord(normalized.mcp)) {
    for (const entry of Object.values(normalized.mcp)) {
      if (!isRecord(entry)) continue
      entry.default_enabled ??= true
      dropEmptyParameters(entry)
    }
  }
  return normalized
}

function dropEmptyParameters(entry: Record<string, unknown>) {
  if (!isRecord(entry.permission)) return
  const { parameters } = entry.permission
  if (isRecord(parameters) && Object.keys(parameters).length === 0) {
    delete entry.permission.parameters
  }
}

function deepEqual(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false
    return a.every((item, index) => deepEqual(item, b[index]))
  }
  if (!isRecord(a) || !isRecord(b)) return false
  const keys = Object.keys(a)
  if (keys.length !== Object.keys(b).length) return false
  return keys.every((key) => key in b && deepEqual(a[key], b[key]))
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function extractBasicConfig(doc: Record<string, unknown>): BasicConfig | null {
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
    // Block scalars parse back with a trailing newline the serializer would
    // trim again; trim here so the seeded instruction matches what was typed.
    instruction: doc.instruction.replace(/\r\n?/g, '\n').trimEnd(),
    providerConfig,
    modelName,
    machineSources,
    tools,
    mcpServers,
    skillIds,
  }
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
  const name = isPool ? entry.machine_pool_name : entry.machine_name
  if (typeof name !== 'string') return null
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
    defaultCwd: typeof entry.cwd === 'string' ? entry.cwd : '',
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
 * option keys account for every entry. Providers sharing a key serialize that
 * entry identically, so a wrong pick still round-trips; the combobox replaces
 * the guess with the granted pool's real provider once it resolves.
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
  if (!isRecord(entry)) return null
  if (entry.type !== undefined && entry.type !== 'built_in') return null
  const permission = extractPermission(entry.permission)
  return permission == null ? null : { name, permission }
}

function extractPermission(value: unknown): ToolPermissionSelection | null {
  if (!isRecord(value) || typeof value.mode !== 'string') return null
  if (value.parameters === undefined) return { mode: value.mode, parameters: {} }
  return isRecord(value.parameters) ? { mode: value.mode, parameters: value.parameters } : null
}

function extractMcpServer(name: string, entry: unknown): BasicMcpServer | null {
  if (!isRecord(entry)) return null
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
  if (type !== 'sigv4') return { ...server, authType: type, secretId }
  if (typeof service !== 'string' || typeof region !== 'string') return null
  return { ...server, authType: type, secretId, service, region }
}
