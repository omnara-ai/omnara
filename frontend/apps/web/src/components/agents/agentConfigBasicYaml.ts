import type { ToolPermissionSelection } from '@omnara/sdk'
import { type Document, isMap, isNode, parse, parseDocument, stringify } from 'yaml'

import type {
  BasicConfig,
  BasicMachineSource,
  BasicMcpServer,
} from '@/components/agents/agentConfigBasic'
import {
  deepEqual,
  extractBasicConfig,
  isRecord,
  normalizeMultiline,
} from '@/components/agents/agentConfigBasicExtract'
import type { BasicTool } from '@/components/agents/AgentConfigToolsField'
import {
  envFromRows,
  providerOptionsOverlay,
  secretEnvFromRows,
} from '@/components/machines/machineOverrides'
import { isMachinePoolProvider } from '@/components/org/machinePoolProviders'

/**
 * Best-effort mapping of a config source into a builder draft. Returns null
 * when the source contains anything the builder cannot faithfully edit:
 * unknown fields inside the entries it rewrites, non-built-in or disabled
 * tools, or an unknown version. Fields outside those entries (unknown
 * top-level keys, extra model keys) are fine — applyBasicConfig never touches
 * them, so the builder can't drop or change them.
 */
export function deserializeBasicConfig(source: string): BasicConfig | null {
  let doc: unknown
  try {
    doc = parse(source)
  } catch {
    return null
  }
  if (!isRecord(doc)) return null
  return extractBasicConfig(doc)
}

/**
 * Applies a builder draft onto the YAML source it was deserialized from,
 * rewriting only the entries whose content actually changed so comments, key
 * order, and formatting everywhere else survive. Returns the source verbatim
 * when the draft still matches it.
 */
export function applyBasicConfig(baselineSource: string, config: BasicConfig): string {
  try {
    const doc = parseDocument(baselineSource)
    if (doc.errors.length > 0 || doc.contents == null || !isMap(doc.contents)) {
      return stringify(wireConfig(config))
    }
    return applyToDocument(doc, baselineSource, config)
  } catch {
    return stringify(wireConfig(config))
  }
}

function applyToDocument(doc: Document, baselineSource: string, config: BasicConfig): string {
  const js: unknown = doc.toJS()
  const baseline = isRecord(js) ? extractBasicConfig(js) : null
  const edits = { count: 0 }
  const set = (path: string[], value: unknown) => {
    const node = doc.createNode(value)
    // Comments before a sequence (e.g. above its first item) live on the
    // sequence node itself; carry them onto the replacement.
    const previous = doc.getIn(path, true)
    if (isNode(previous) && isNode(node)) {
      node.commentBefore = previous.commentBefore
      node.comment = previous.comment
    }
    doc.setIn(path, node)
    edits.count += 1
  }
  const del = (path: (string | number)[]) => {
    if (doc.deleteIn(path)) edits.count += 1
  }

  const instruction = normalizeMultiline(config.instruction)
  if (instruction !== baseline?.instruction) set(['instruction'], instruction)
  const providerConfig = config.providerConfig.trim()
  if (providerConfig !== baseline?.providerConfig) set(['model', 'provider_config'], providerConfig)
  const modelName = config.modelName.trim()
  if (modelName !== baseline?.modelName) set(['model', 'name'], modelName)

  applyMachineSources(doc, config.machineSources, baseline?.machineSources ?? null, set, del)
  applyNamedEntries(
    'tools',
    config.tools.map((tool) => ({
      name: tool.name.trim(),
      comparable: permissionComparable(tool.permission),
      wire: () => toolWire(tool),
    })),
    baseline == null
      ? null
      : baseline.tools.map((tool) => [tool.name, permissionComparable(tool.permission)]),
    set,
    del,
  )
  applySkills(config.skillIds, baseline?.skillIds ?? null, set, del)
  applyNamedEntries(
    'mcp',
    config.mcpServers.map((server) => ({
      name: server.name.trim(),
      comparable: mcpComparable(server),
      wire: () => mcpWire(server),
    })),
    baseline == null
      ? null
      : baseline.mcpServers.map((server) => [server.name, mcpComparable(server)]),
    set,
    del,
  )

  return edits.count > 0 ? doc.toString() : baselineSource
}

interface NamedEntry {
  name: string
  comparable: unknown
  wire: () => unknown
}

function applyNamedEntries(
  key: string,
  desired: NamedEntry[],
  baseline: [string, unknown][] | null,
  set: (path: string[], value: unknown) => void,
  del: (path: string[]) => void,
) {
  const baselineByName = new Map(baseline ?? [])
  if (desired.length === 0) {
    if (baseline == null || baselineByName.size > 0) del([key])
    return
  }
  const desiredNames = new Set(desired.map((entry) => entry.name))
  for (const name of baselineByName.keys()) {
    if (!desiredNames.has(name)) del([key, name])
  }
  for (const entry of desired) {
    const base = baselineByName.get(entry.name)
    if (base !== undefined && deepEqual(base, entry.comparable)) continue
    set([key, entry.name], entry.wire())
  }
}

function applyMachineSources(
  doc: Document,
  rows: BasicMachineSource[],
  baselineRows: BasicMachineSource[] | null,
  set: (path: string[], value: unknown) => void,
  del: (path: string[]) => void,
) {
  const desired = rows.map(machineSourceComparable)
  const baseline = baselineRows?.map(machineSourceComparable) ?? null
  if (baseline != null && deepEqual(desired, baseline)) return
  if (rows.length === 0) {
    del(['machine_sources'])
    return
  }
  const items = rows.map((row, index) => {
    if (baseline != null && index < baseline.length && deepEqual(desired[index], baseline[index])) {
      return doc.getIn(['machine_sources', index], true)
    }
    return machineSourceWire(row)
  })
  set(['machine_sources'], items)
}

function applySkills(
  skillIds: string[],
  baselineIds: string[] | null,
  set: (path: string[], value: unknown) => void,
  del: (path: string[]) => void,
) {
  if (baselineIds != null && deepEqual(skillIds, baselineIds)) return
  if (skillIds.length === 0) {
    del(['skills'])
    return
  }
  set(['skills'], [...skillIds])
}

/**
 * Change detection compares these normalized shapes, not serialized output, so
 * a pool-resolution backfill of provider/managementKind never rewrites a row
 * the user didn't touch.
 */
function machineSourceComparable(source: BasicMachineSource) {
  return {
    kind: source.kind,
    name: source.name.trim(),
    cwd: source.defaultCwd.trim(),
    initialNumMachines: source.initialNumMachines,
    maxMachines: source.maxMachines,
    machineCpu: source.machineCpu,
    machineMemoryMb: source.machineMemoryMb,
    providerOptions: source.providerOptions,
    env: envFromRows(source.envRows) ?? null,
    secretEnv: secretEnvFromRows(source.secretEnvRows) ?? null,
  }
}

function permissionComparable(permission: ToolPermissionSelection) {
  return { mode: permission.mode, parameters: permission.parameters }
}

function mcpComparable(server: BasicMcpServer) {
  return {
    url: server.url.trim(),
    permission: permissionComparable(server.permission),
    defaultEnabled: server.defaultEnabled,
    authType: server.authType,
    secretId: server.authType === 'none' ? '' : server.secretId.trim(),
    service: server.authType === 'sigv4' ? server.service.trim() : '',
    region: server.authType === 'sigv4' ? server.region.trim() : '',
  }
}

function wireConfig(config: BasicConfig): Record<string, unknown> {
  const wire: Record<string, unknown> = {
    instruction: normalizeMultiline(config.instruction),
    model: {
      provider_config: config.providerConfig.trim(),
      name: config.modelName.trim(),
    },
  }
  if (config.machineSources.length > 0) {
    wire.machine_sources = config.machineSources.map(machineSourceWire)
  }
  if (config.tools.length > 0) {
    wire.tools = Object.fromEntries(config.tools.map((tool) => [tool.name.trim(), toolWire(tool)]))
  }
  if (config.skillIds.length > 0) wire.skills = [...config.skillIds]
  if (config.mcpServers.length > 0) {
    wire.mcp = Object.fromEntries(
      config.mcpServers.map((server) => [server.name.trim(), mcpWire(server)]),
    )
  }
  return wire
}

function machineSourceWire(source: BasicMachineSource): Record<string, unknown> {
  const wire: Record<string, unknown> = {}
  if (source.kind === 'pool') {
    wire.machine_pool_name = source.name.trim()
    if (source.initialNumMachines !== '') {
      wire.initial_num_machines = Number(source.initialNumMachines)
    }
    if (source.maxMachines !== '') wire.max_machines = Number(source.maxMachines)
    if (source.machineCpu !== '') wire.machine_cpu = Number(source.machineCpu)
    if (source.machineMemoryMb !== '') wire.machine_memory_mb = Number(source.machineMemoryMb)
    const optionsOverlay = isMachinePoolProvider(source.provider)
      ? providerOptionsOverlay(
          source.provider,
          source.providerOptions,
          source.managementKind === 'cluster',
        )
      : undefined
    if (optionsOverlay) wire.machine_provider_options_overlay = optionsOverlay
  } else {
    wire.machine_name = source.name.trim()
  }
  if (source.defaultCwd.trim() !== '') wire.cwd = source.defaultCwd.trim()
  const envOverlay = envFromRows(source.envRows)
  if (envOverlay) wire.env_overlay = envOverlay
  const secretEnvOverlay = secretEnvFromRows(source.secretEnvRows)
  if (secretEnvOverlay) wire.secret_env_overlay = secretEnvOverlay
  return wire
}

function toolWire(tool: BasicTool): Record<string, unknown> {
  return { type: 'built_in', permission: permissionWire(tool.permission) }
}

function mcpWire(server: BasicMcpServer): Record<string, unknown> {
  const wire: Record<string, unknown> = {
    url: server.url.trim(),
    permission: permissionWire(server.permission),
    default_enabled: server.defaultEnabled,
  }
  if (server.authType !== 'none') {
    const auth: Record<string, string> = {
      type: server.authType,
      secret_id: server.secretId.trim(),
    }
    if (server.authType === 'sigv4') {
      auth.service = server.service.trim()
      auth.region = server.region.trim()
    }
    wire.auth = auth
  }
  return wire
}

function permissionWire(permission: ToolPermissionSelection): Record<string, unknown> {
  return Object.keys(permission.parameters).length > 0
    ? { mode: permission.mode, parameters: permission.parameters }
    : { mode: permission.mode }
}
