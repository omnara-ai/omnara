import { type Document, isMap, isNode, isScalar, type Node, parseDocument } from 'yaml'
import { z } from 'zod'

import {
  type MachineEntry,
  normalizeMultiline,
  type PoolEntry,
  type ToolEntry,
} from '@/components/agents/agentConfigBasicExtract'
import { mcpWire, permissionWire } from '@/components/agents/agentConfigMcp'
import { patchConfigSource } from '@/components/agents/agentConfigSourcePatch'
import { subagentWire } from '@/components/agents/agentConfigSubagents'
import type { BasicTool } from '@/components/agents/AgentConfigToolsField'
import type { BasicConfig, BasicMachineSource } from '@/components/agents/useAgentBuilderForm'
import { overlayFromSecretRows, overlayFromTextRows } from '@/components/key-value/keyValueRows'
import { providerOptionsOverlay } from '@/components/machines/machineOverrides'
import { isMachinePoolProvider } from '@/components/org/machinePoolProviders'
import { memoryGbToMb } from '@/lib/machine-memory'
import { normalizeResourceName } from '@/lib/resource-name'

type WireValue = string | number | boolean | null | undefined | WireValue[] | WireObject

interface WireObject {
  [key: string]: WireValue
}

type YamlInput = WireValue | Node | YamlInput[]

type Setter = (path: string[], value: YamlInput) => void
type Deleter = (path: (string | number)[]) => void

export function applyToDocument(
  doc: Document,
  baselineSource: string,
  baseline: BasicConfig | null,
  config: BasicConfig,
): string {
  const original = doc.clone()
  const edits = { count: 0 }
  const protectedPaths = baseline?.advancedEntries?.map((entry) => entry.path) ?? []
  const protectedPath = (path: (string | number)[]) =>
    protectedPaths.some(
      (owned) =>
        owned.every((part, index) => path[index] === part) ||
        path.every((part, index) => owned[index] === part),
    )
  const set: Setter = (path, value) => {
    if (protectedPath(path)) return
    for (let length = 1; length < path.length; length += 1) {
      const parentPath = path.slice(0, length)
      const parent = doc.getIn(parentPath, true)
      if (isScalar(parent) && parent.value === null) doc.setIn(parentPath, doc.createNode({}))
    }
    const node = doc.createNode(value)
    const previous = doc.getIn(path, true)
    if (isNode(previous) && isNode(node)) {
      node.commentBefore = previous.commentBefore
      node.comment = previous.comment
    }
    doc.setIn(path, node)
    edits.count += 1
  }
  const del: Deleter = (path) => {
    if (protectedPath(path)) return
    if (doc.deleteIn(path)) {
      edits.count += 1
      const parent = doc.getIn(path.slice(0, -1), true)
      if (path.length === 2 && isMap(parent) && parent.items.length === 0)
        doc.deleteIn(path.slice(0, -1))
    }
  }

  const applyList = (key: string, items: WireValue[], baseline: WireValue[] | null) => {
    if (baseline != null && deepEqual(items, baseline)) return
    if (items.length === 0) del([key])
    else set([key], items)
  }

  const instruction = normalizeMultiline(config.instruction)
  if (instruction !== (baseline?.instruction ?? '')) set(['instruction'], instruction)
  const providerConfig = normalizeResourceName(config.providerConfig)
  if (providerConfig !== normalizeResourceName(baseline?.providerConfig ?? '')) {
    set(['model', 'provider_config'], providerConfig)
  }
  const modelName = normalizeResourceName(config.modelName)
  if (modelName !== normalizeResourceName(baseline?.modelName ?? ''))
    set(['model', 'name'], modelName)
  const reasoningEffort = config.reasoningEffort.trim()
  if (reasoningEffort !== (baseline?.reasoningEffort ?? '')) {
    if (reasoningEffort === '') {
      del(['model', 'reasoning', 'effort'])
      const reasoning = doc.getIn(['model', 'reasoning'], true)
      if (isMap(reasoning) && reasoning.items.length === 0) del(['model', 'reasoning'])
    } else set(['model', 'reasoning', 'effort'], reasoningEffort)
  }

  applyMachineSources(doc, config.machineSources, baseline?.machineSources ?? null, set, del)
  applyNamedEntries(
    'tools',
    config.tools.map((tool) => [tool.name, toolWire(tool)]),
    baseline == null ? null : baseline.tools.map((tool) => [tool.name, toolWire(tool)]),
    set,
    del,
  )
  applyNamedEntries(
    'interaction_handlers',
    Object.entries(config.interactionHandlers),
    baseline == null ? null : Object.entries(baseline.interactionHandlers),
    set,
    del,
  )
  if (!deepEqual(config.gitCredentials, baseline?.gitCredentials)) {
    if (config.gitCredentials == null) del(['git_credentials'])
    else set(['git_credentials'], config.gitCredentials)
  }
  applyList('memory_stores', config.memoryStores, baseline?.memoryStores ?? null)
  applyList('skills', config.skillIds, baseline?.skillIds ?? null)
  applyNamedEntries(
    'subagents',
    config.subagents.map((subagent) => [subagent.key, subagentWire(subagent)]),
    baseline == null
      ? null
      : baseline.subagents.map((subagent) => [subagent.key, subagentWire(subagent)]),
    set,
    del,
  )
  if (config.maxSubagents !== (baseline?.maxSubagents ?? '')) {
    if (config.maxSubagents === '') del(['max_subagents'])
    else set(['max_subagents'], Number(config.maxSubagents))
  }
  if (config.maxDepth !== (baseline?.maxDepth ?? '')) {
    if (config.maxDepth === '') del(['max_depth'])
    else set(['max_depth'], Number(config.maxDepth))
  }
  applyNamedEntries(
    'mcp',
    config.mcpServers.map((server) => [server.name, mcpWire(server)]),
    baseline == null ? null : baseline.mcpServers.map((server) => [server.name, mcpWire(server)]),
    set,
    del,
  )

  const rawEdits: string[][] = []
  for (const entry of config.advancedEntries ?? []) {
    const previous = baseline?.advancedEntries?.find(
      (item) => item.path.join('\0') === entry.path.join('\0'),
    )
    if (previous && previous.source !== entry.source) {
      rawEdits.push(entry.path)
      doc.setIn(entry.path, parseDocument(entry.source).contents)
      edits.count += 1
    }
  }
  if (edits.count === 0) return baselineSource
  if (baseline == null) return doc.toString()
  const result = patchConfigSource(baselineSource, original, doc, rawEdits)
  const reparsed = parseDocument(result)
  if (
    reparsed.errors.length > 0 ||
    !deepEqual(z.json().parse(reparsed.toJS()), z.json().parse(doc.toJS()))
  ) {
    throw new Error('The Builder cannot safely patch this edit. Use YAML to edit this definition.')
  }
  return result
}

function applyNamedEntries(
  key: string,
  desired: [string, WireValue][],
  baseline: [string, WireValue][] | null,
  set: Setter,
  del: Deleter,
) {
  const baselineByName = new Map(baseline ?? [])
  const desiredNames = new Set(desired.map(([name]) => name))
  for (const name of baselineByName.keys()) {
    if (!desiredNames.has(name)) del([key, name])
  }
  for (const [name, wire] of desired) {
    const base = baselineByName.get(name)
    if (base !== undefined && deepEqual(base, wire)) continue
    applyWireChanges([key, name], base, wire, set, del)
  }
}

// Compare against the draft projection, not defaults serialized from it. A permission
// edit must not add type/default_enabled or normalize other untouched fields.
function applyWireChanges(
  path: string[],
  baseline: WireValue,
  desired: WireValue,
  set: Setter,
  del: Deleter,
) {
  if (deepEqual(baseline, desired)) return
  if (desired === undefined) {
    del(path)
    return
  }
  if (isWireObject(baseline) && isWireObject(desired)) {
    for (const key of new Set([...Object.keys(baseline), ...Object.keys(desired)])) {
      applyWireChanges([...path, key], baseline[key], desired[key], set, del)
    }
  } else set(path, desired)
}

function applyMachineSources(
  doc: Document,
  rows: BasicMachineSource[],
  baselineRows: BasicMachineSource[] | null,
  set: Setter,
  del: Deleter,
) {
  const desired = rows.map(machineSourceComparable)
  const baseline = baselineRows?.map(machineSourceComparable) ?? null
  if (baseline != null && deepEqual(desired, baseline)) return
  if (rows.length === 0) {
    del(['machine_sources'])
    return
  }
  const items = rows.map((row, index): YamlInput => {
    if (baseline != null && index < baseline.length && deepEqual(desired[index], baseline[index])) {
      const existing = doc.getIn(['machine_sources', index], true)
      if (isNode(existing)) return existing
    }
    return machineSourceWire(row)
  })
  set(['machine_sources'], items)
}

function machineSourceComparable(source: BasicMachineSource) {
  return {
    kind: source.kind,
    name: normalizeResourceName(source.name),
    cwd: source.defaultCwd.trim(),
    initialNumMachines: source.initialNumMachines,
    maxMachines: source.maxMachines,
    deleteAfterIdleMinutes: source.deleteAfterIdleMinutes,
    machineCpu: source.machineCpu,
    machineMemoryGb: source.machineMemoryGb,
    providerOptions: { ...source.providerOptions },
    env: overlayFromTextRows(source.envRows) ?? null,
    secretEnv: overlayFromSecretRows(source.secretEnvRows) ?? null,
  }
}

function isWireObject(value: WireValue): value is WireObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function deepEqual(a: WireValue, b: WireValue): boolean {
  if (Object.is(a, b)) return true
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false
    return a.every((item, index) => deepEqual(item, b[index]))
  }
  if (!isWireObject(a) || !isWireObject(b)) return false
  const keys = Object.keys(a)
  if (keys.length !== Object.keys(b).length) return false
  return keys.every((key) => key in b && deepEqual(a[key], b[key]))
}

function machineSourceWire(source: BasicMachineSource): PoolEntry | MachineEntry {
  const name = normalizeResourceName(source.name)
  if (source.kind !== 'pool') {
    const wire: MachineEntry = { machine_name: name }
    applySourceOverlays(wire, source)
    return wire
  }
  const wire: PoolEntry = { machine_pool_name: name }
  if (source.initialNumMachines !== '') {
    wire.initial_num_machines = Number(source.initialNumMachines)
  }
  if (source.maxMachines !== '') wire.max_machines = Number(source.maxMachines)
  if (source.deleteAfterIdleMinutes !== '') {
    wire.delete_after_idle_minutes = Number(source.deleteAfterIdleMinutes)
  }
  if (source.machineCpu !== '') wire.machine_cpu = Number(source.machineCpu)
  if (source.machineMemoryGb !== '') wire.machine_memory_mb = memoryGbToMb(source.machineMemoryGb)
  const optionsOverlay = isMachinePoolProvider(source.provider)
    ? providerOptionsOverlay(source.provider, source.providerOptions)
    : undefined
  if (optionsOverlay) wire.machine_provider_options_overlay = optionsOverlay
  applySourceOverlays(wire, source)
  return wire
}

function applySourceOverlays(wire: PoolEntry | MachineEntry, source: BasicMachineSource) {
  if (source.defaultCwd.trim() !== '') wire.cwd = source.defaultCwd.trim()
  const envOverlay = overlayFromTextRows(source.envRows)
  if (envOverlay) wire.env_overlay = envOverlay
  const secretEnvOverlay = overlayFromSecretRows(source.secretEnvRows)
  if (secretEnvOverlay) wire.secret_env_overlay = secretEnvOverlay
}

export function toolWire(tool: BasicTool): ToolEntry {
  const wire: ToolEntry = tool.name.startsWith('int__') ? {} : { type: 'built_in' }
  if (tool.enabled === false) wire.enabled = false
  if (tool.permission != null) wire.permission = permissionWire(tool.permission)
  if (tool.deferred) wire.deferred = true
  return wire
}
