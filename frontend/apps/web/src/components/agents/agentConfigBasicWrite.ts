import { type Document, isMap, isSeq, type Node } from 'yaml'

import {
  type MachineEntry,
  normalizeMultiline,
  type PoolEntry,
  type ToolEntry,
} from '@/components/agents/agentConfigBasicExtract'
import {
  ConfigEditError,
  deepEqual,
  editConfigDocument,
} from '@/components/agents/agentConfigDocument'
import { mcpWire, permissionWire } from '@/components/agents/agentConfigMcp'
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
  original: Document,
  baselineSource: string,
  baseline: BasicConfig | null,
  config: BasicConfig,
): string {
  const transaction = editConfigDocument(
    baselineSource,
    original,
    baseline?.advancedEntries?.map((entry) => entry.path) ?? [],
  )
  const { set, remove: del } = transaction

  const applyList = (key: string, items: WireValue[], base: WireValue[] | null) => {
    if (base == null) {
      if (items.length > 0) set([key], items)
      return
    }
    const identity = (value: WireValue) => JSON.stringify(isWireObject(value) ? value.name : value)
    const desired = new Map(items.map((value) => [identity(value), value]))
    const baselineIds = new Set(base.map(identity))
    const removed: number[] = []
    base.forEach((value, index) => {
      const next = desired.get(identity(value))
      if (next === undefined) removed.push(index)
      else applyWireChanges([key, String(index)], value, next, set, del)
    })
    for (const index of removed.reverse()) del([key, index])
    for (const value of items) {
      if (baselineIds.has(identity(value))) continue
      const sequence = transaction.document.getIn([key], true)
      if (isSeq(sequence)) set([key, String(sequence.items.length)], value)
      else set([key], [value])
    }
    const sequence = transaction.document.getIn([key], true)
    if (removed.length > 0 && isSeq(sequence) && sequence.items.length === 0) del([key])
    transaction.orderSequence(
      [key],
      [
        ...base.filter((value) => desired.has(identity(value))),
        ...items.filter((value) => !baselineIds.has(identity(value))),
      ].map(identity),
      items.map(identity),
    )
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
      const reasoning = transaction.document.getIn(['model', 'reasoning'], true)
      if (isMap(reasoning) && reasoning.items.length === 0) del(['model', 'reasoning'])
    } else set(['model', 'reasoning', 'effort'], reasoningEffort)
  }

  applyMachineSources(transaction, config.machineSources, baseline?.machineSources ?? null)
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

  for (const entry of config.advancedEntries ?? []) {
    const previous = baseline?.advancedEntries?.find(
      (item) => item.path.join('\0') === entry.path.join('\0'),
    )
    if (entry.removed) {
      if (previous?.kind !== 'custom' || entry.path.length !== 2 || entry.path[0] !== 'tools')
        throw new ConfigEditError(entry.path, 'Only an existing custom tool can be removed here.')
      transaction.removeRawDefinition(entry.path)
      continue
    }
    if (
      (previous && previous.source !== entry.source) ||
      (!previous && entry.kind === 'custom' && entry.path.length === 2 && entry.path[0] === 'tools')
    ) {
      transaction.setRawDefinition(entry.path, entry.source)
    }
  }
  return transaction.serialize()
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
    for (const key of Object.keys(desired)) {
      if (desired[key] !== undefined)
        applyWireChanges([...path, key], baseline[key], desired[key], set, del)
    }
    for (const key of Object.keys(baseline)) {
      if (desired[key] === undefined) del([...path, key])
    }
  } else set(path, desired)
}

function applyMachineSources(
  transaction: ReturnType<typeof editConfigDocument>,
  rows: BasicMachineSource[],
  baselineRows: BasicMachineSource[] | null,
) {
  const { set, remove: del } = transaction
  if (baselineRows == null) {
    if (rows.length > 0) set(['machine_sources'], rows.map(machineSourceWire))
    return
  }
  const baselineById = new Map(baselineRows.map((row) => [row.id, row]))
  const desiredIds = new Set(rows.map((row) => row.id))
  for (const row of rows) {
    const base = baselineById.get(row.id)
    if (!base) continue
    if (deepEqual(machineSourceComparable(base), machineSourceComparable(row))) continue
    if (base.sourceIndex === undefined) throw new Error('Missing machine source identity.')
    applyWireChanges(
      ['machine_sources', String(base.sourceIndex)],
      machineSourceWire(base),
      machineSourceWire(row),
      set,
      del,
    )
  }
  const removed = baselineRows
    .filter((row) => !desiredIds.has(row.id))
    .sort((a, b) => (b.sourceIndex ?? 0) - (a.sourceIndex ?? 0))
  for (const row of removed) {
    if (row.sourceIndex !== undefined) del(['machine_sources', row.sourceIndex])
  }
  for (const row of rows) {
    if (baselineById.has(row.id)) continue
    const sequence = transaction.document.getIn(['machine_sources'], true)
    if (isSeq(sequence))
      set(['machine_sources', String(sequence.items.length)], machineSourceWire(row))
    else set(['machine_sources'], [machineSourceWire(row)])
  }
  const sequence = transaction.document.getIn(['machine_sources'], true)
  if (removed.length > 0 && isSeq(sequence) && sequence.items.length === 0) del(['machine_sources'])
  transaction.orderSequence(
    ['machine_sources'],
    [
      ...baselineRows.filter((row) => desiredIds.has(row.id)),
      ...rows.filter((row) => !baselineById.has(row.id)),
    ].map((row) => row.id),
    rows.map((row) => row.id),
  )
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
