import type { ToolPermissionSelection } from '@omnara/sdk'
import { Document, isMap, isNode, parseDocument } from 'yaml'

import type {
  BasicConfig,
  BasicMachineSource,
  BasicMcpServer,
} from '@/components/agents/agentConfigBasic'
import { extractBasicConfig, normalizeMultiline } from '@/components/agents/agentConfigBasicExtract'
import type { BasicTool } from '@/components/agents/AgentConfigToolsField'
import {
  envFromRows,
  providerOptionsOverlay,
  secretEnvFromRows,
} from '@/components/machines/machineOverrides'
import { isMachinePoolProvider } from '@/components/org/machinePoolProviders'

export interface BasicConfigSession {
  readonly initialDraft: BasicConfig | null
  apply(config: BasicConfig): string
}

export function createBasicConfigSession(source: string): BasicConfigSession {
  const doc = parseSourceDocument(source)
  const initialDraft = doc == null ? null : extractBasicConfig(doc.toJS())
  return {
    initialDraft,
    apply(config) {
      return applyToDocument(doc?.clone() ?? new Document({}), source, initialDraft, config)
    },
  }
}

function parseSourceDocument(source: string): Document | null {
  try {
    const doc = parseDocument(source)
    if (doc.errors.length > 0 || doc.contents == null || !isMap(doc.contents)) return null
    return doc
  } catch {
    return null
  }
}

function applyToDocument(
  doc: Document,
  baselineSource: string,
  baseline: BasicConfig | null,
  config: BasicConfig,
): string {
  const edits = { count: 0 }
  const set = (path: string[], value: unknown) => {
    const node = doc.createNode(value)
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
    config.tools.map((tool) => [tool.name.trim(), toolWire(tool)]),
    baseline == null ? null : baseline.tools.map((tool) => [tool.name, toolWire(tool)]),
    set,
    del,
  )
  applySkills(config.skillIds, baseline?.skillIds ?? null, set, del)
  applyNamedEntries(
    'mcp',
    config.mcpServers.map((server) => [server.name.trim(), mcpWire(server)]),
    baseline == null ? null : baseline.mcpServers.map((server) => [server.name, mcpWire(server)]),
    set,
    del,
  )

  return edits.count > 0 ? doc.toString() : baselineSource
}

function applyNamedEntries(
  key: string,
  desired: [string, unknown][],
  baseline: [string, unknown][] | null,
  set: (path: string[], value: unknown) => void,
  del: (path: string[]) => void,
) {
  const baselineByName = new Map(baseline ?? [])
  if (desired.length === 0) {
    if (baseline == null || baselineByName.size > 0) del([key])
    return
  }
  const desiredNames = new Set(desired.map(([name]) => name))
  for (const name of baselineByName.keys()) {
    if (!desiredNames.has(name)) del([key, name])
  }
  for (const [name, wire] of desired) {
    const base = baselineByName.get(name)
    if (base !== undefined && deepEqual(base, wire)) continue
    set([key, name], wire)
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

function deepEqual(a: unknown, b: unknown, depth = 0): boolean {
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

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
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
