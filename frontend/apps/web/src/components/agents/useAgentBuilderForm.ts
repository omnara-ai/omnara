import { type ConfigAppCapabilitySource } from '@omnara/sdk'
import { useState } from 'react'
import { Document, isMap, isNode, type Node, parseDocument } from 'yaml'

import {
  extractBasicConfig,
  type MachineEntry,
  normalizeMultiline,
  type PoolEntry,
  type ToolEntry,
} from '@/components/agents/agentConfigBasicExtract'
import { eventWebhookUrlError, eventWebhookWire } from '@/components/agents/agentConfigEventWebhook'
import {
  type BasicMcpServer,
  type BasicMcpTool,
  type McpAuthType,
  mcpServerNamesUnique,
  mcpServerValid,
  mcpWire,
  permissionWire,
} from '@/components/agents/agentConfigMcp'
import type { ModelSelection } from '@/components/agents/AgentConfigModelField'
import {
  type BasicSubagent,
  subagentsValid,
  subagentWire,
} from '@/components/agents/agentConfigSubagents'
import type { BasicTool } from '@/components/agents/AgentConfigToolsField'
import { useAgentBuilderTools } from '@/components/agents/useAgentBuilderTools'
import {
  emptyProviderOptions,
  envOverlayFromRows,
  type EnvOverlayRow,
  envOverlayRowsValid,
  optionalIdleDeletionMinutesValid,
  optionalPositiveInt32Valid,
  type ProviderOptionsDraft,
  providerOptionsOverlay,
  secretEnvOverlayFromRows,
  type SecretEnvOverlayRow,
  secretEnvOverlayRowsValid,
} from '@/components/machines/machineOverrides'
import { isMachinePoolProvider } from '@/components/org/machinePoolProviders'
import { memoryGbDraftValid, memoryGbToMb } from '@/lib/machine-memory'
import { normalizeResourceName, resourceNameValid } from '@/lib/resource-name'

export { type BasicMcpServer, type BasicMcpTool, type McpAuthType }
export {
  mcpRuntimeToolName,
  mcpRuntimeToolNameError,
  mcpRuntimeToolNameMaxLength,
  mcpServerNameError,
  mcpServerNameMaxLength,
  mcpToolEnabled,
  mcpToolNameAddable,
  type UnexposableMcpTool,
  unexposableMcpTools,
} from '@/components/agents/agentConfigMcp'

export type MachineSourceKind = 'pool' | 'machine'

export interface BasicMachineSource {
  id: string
  kind: MachineSourceKind
  name: string
  provider: string
  managementKind: string
  defaultCwd: string
  initialNumMachines: string
  maxMachines: string
  deleteAfterIdleMinutes: string
  machineCpu: string
  machineMemoryGb: string
  providerOptions: ProviderOptionsDraft
  envRows: EnvOverlayRow[]
  secretEnvRows: SecretEnvOverlayRow[]
}

export interface BasicConfig {
  instruction: string
  providerConfig: string
  modelName: string
  machineSources: BasicMachineSource[]
  tools: BasicTool[]
  interactionHandlers: Record<string, ConfigAppCapabilitySource>
  mcpServers: BasicMcpServer[]
  eventWebhookEvents: string[]
  eventWebhookUrl: string
  eventWebhookSigningSecretId: string
  skillIds: string[]
  subagents: BasicSubagent[]
  maxSubagents: string
  maxDepth: string
}

export function newMachineSource(kind: MachineSourceKind): BasicMachineSource {
  return {
    id: crypto.randomUUID(),
    kind,
    name: '',
    provider: '',
    managementKind: '',
    defaultCwd: '',
    initialNumMachines: '',
    maxMachines: '',
    deleteAfterIdleMinutes: '',
    machineCpu: '',
    machineMemoryGb: '',
    providerOptions: emptyProviderOptions,
    envRows: [],
    secretEnvRows: [],
  }
}

export const emptyBasicConfig: BasicConfig = {
  instruction: '',
  providerConfig: '',
  modelName: '',
  machineSources: [],
  tools: [],
  interactionHandlers: {},
  mcpServers: [],
  eventWebhookEvents: ['tool_call_update'],
  eventWebhookUrl: '',
  eventWebhookSigningSecretId: '',
  skillIds: [],
  subagents: [],
  maxSubagents: '',
  maxDepth: '',
}

export interface BasicConfigSession {
  readonly initialDraft: BasicConfig | null
  apply(config: BasicConfig): string
}

export function createBasicConfigSession(source: string): BasicConfigSession {
  const doc = parseSourceDocument(source)
  const initialDraft = doc == null ? null : extractBasicConfig(doc)
  return {
    initialDraft,
    apply(config) {
      if (doc != null && initialDraft == null) return source
      return applyToDocument(doc?.clone() ?? new Document({}), source, initialDraft, config)
    },
  }
}

export type AgentBuilderForm = ReturnType<typeof useAgentBuilderForm>

export function useAgentBuilderForm(
  session: BasicConfigSession,
  seedConfig: BasicConfig | undefined,
  scope: { orgId: string; projectId: string },
) {
  const [draft, setDraft] = useState<BasicConfig>(
    seedConfig ?? session.initialDraft ?? emptyBasicConfig,
  )
  const tools = useAgentBuilderTools(draft, scope)
  const [unavailableSkillIds, setUnavailableSkillIds] = useState<string[]>([])
  const [unavailableSourceIds, setUnavailableSourceIds] = useState<string[]>([])
  const [modelUnavailable, setModelUnavailable] = useState(false)

  const blocked =
    unavailableSkillIds.length > 0 ||
    unavailableSourceIds.length > 0 ||
    modelUnavailable ||
    !basicConfigValid(draft)

  const patch = (fields: Partial<BasicConfig>) => {
    setDraft((prev) => ({ ...prev, ...fields }))
  }

  return {
    draft,
    yaml: session.apply(draft),
    blocked,
    resolvedTools: tools.data?.tools,
    toolsPending: tools.isPending,
    toolsError: tools.isError,
    toolsErrorMessage: tools.errorMessage,
    retryTools: () => void tools.refetch(),
    reset: (config: BasicConfig | null) => {
      setDraft(config ?? emptyBasicConfig)
    },
    instruction: draft.instruction,
    model: { providerConfig: draft.providerConfig, modelName: draft.modelName },
    machineSources: draft.machineSources,
    tools: draft.tools,
    interactionHandlers: draft.interactionHandlers,
    skillIds: draft.skillIds,
    mcpServers: draft.mcpServers,
    eventWebhookEvents: draft.eventWebhookEvents,
    eventWebhookUrl: draft.eventWebhookUrl,
    eventWebhookSigningSecretId: draft.eventWebhookSigningSecretId,
    subagents: draft.subagents,
    maxSubagents: draft.maxSubagents,
    maxDepth: draft.maxDepth,
    setInstruction: (instruction: string) => {
      patch({ instruction })
    },
    setModel: (model: ModelSelection) => {
      patch({ providerConfig: model.providerConfig, modelName: model.modelName })
    },
    setMachineSources: (machineSources: BasicMachineSource[]) => {
      patch({ machineSources })
    },
    setTools: (tools: BasicTool[]) => {
      patch({ tools })
    },
    setSkillIds: (skillIds: string[]) => {
      patch({ skillIds })
    },
    setMcpServers: (mcpServers: BasicMcpServer[]) => {
      patch({ mcpServers })
    },
    setEventWebhookEvents: (eventWebhookEvents: string[]) => {
      patch({ eventWebhookEvents })
    },
    setEventWebhookUrl: (eventWebhookUrl: string) => {
      patch({ eventWebhookUrl })
    },
    setEventWebhookSigningSecretId: (eventWebhookSigningSecretId: string) => {
      patch({ eventWebhookSigningSecretId })
    },
    setSubagents: (subagents: BasicSubagent[]) => {
      patch(
        subagents.length === 0
          ? {
              subagents,
              maxSubagents: '',
              maxDepth: '',
              tools: draft.tools.filter((tool) => tool.name !== 'spawn_agent'),
            }
          : { subagents },
      )
    },
    setMaxSubagents: (maxSubagents: string) => {
      patch({ maxSubagents })
    },
    setMaxDepth: (maxDepth: string) => {
      patch({ maxDepth })
    },
    reportModelUnavailable: setModelUnavailable,
    reportUnavailableSourceIds: setUnavailableSourceIds,
    reportUnavailableSkillIds: setUnavailableSkillIds,
  }
}

export function basicConfigValid(draft: BasicConfig) {
  return (
    draft.instruction.trim() !== '' &&
    eventWebhookUrlError(draft.eventWebhookUrl) === undefined &&
    (draft.eventWebhookUrl.trim() === '' || draft.eventWebhookEvents.length > 0) &&
    resourceNameValid(draft.providerConfig) &&
    resourceNameValid(draft.modelName) &&
    draft.machineSources.every(machineSourceValid) &&
    mcpServerNamesUnique(draft.mcpServers) &&
    draft.mcpServers.every(mcpServerValid) &&
    subagentsValid(draft.subagents, draft.maxSubagents, draft.maxDepth)
  )
}

const nonNegativeIntegerPattern = /^(0|[1-9][0-9]*)$/

function machineCountValid(value: string) {
  return value === '' || nonNegativeIntegerPattern.test(value)
}

function machineSourceValid(source: BasicMachineSource) {
  return (
    resourceNameValid(source.name) &&
    envOverlayRowsValid(source.envRows) &&
    secretEnvOverlayRowsValid(source.secretEnvRows) &&
    (source.kind === 'machine' ||
      (machineCountValid(source.initialNumMachines) &&
        machineCountValid(source.maxMachines) &&
        optionalIdleDeletionMinutesValid(source.deleteAfterIdleMinutes) &&
        optionalPositiveInt32Valid(source.machineCpu) &&
        memoryGbDraftValid(source.machineMemoryGb, { optional: true })))
  )
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

type WireValue = string | number | boolean | null | undefined | WireValue[] | WireObject

interface WireObject {
  [key: string]: WireValue
}

type YamlInput = WireValue | Node | YamlInput[]

type Setter = (path: string[], value: YamlInput) => void
type Deleter = (path: (string | number)[]) => void

function applyToDocument(
  doc: Document,
  baselineSource: string,
  baseline: BasicConfig | null,
  config: BasicConfig,
): string {
  const edits = { count: 0 }
  const set: Setter = (path, value) => {
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
    if (doc.deleteIn(path)) edits.count += 1
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
  applySkills(config.skillIds, baseline?.skillIds ?? null, set, del)
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

  const eventWebhook = eventWebhookWire(config)
  if (!deepEqual(eventWebhook, baseline ? eventWebhookWire(baseline) : null)) {
    if (eventWebhook) set(['event_webhook'], eventWebhook)
    else del(['event_webhook'])
  }

  return edits.count > 0 ? doc.toString() : baselineSource
}

function applyNamedEntries(
  key: string,
  desired: [string, WireValue][],
  baseline: [string, WireValue][] | null,
  set: Setter,
  del: Deleter,
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

function applySkills(skillIds: string[], baselineIds: string[] | null, set: Setter, del: Deleter) {
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
    name: normalizeResourceName(source.name),
    cwd: source.defaultCwd.trim(),
    initialNumMachines: source.initialNumMachines,
    maxMachines: source.maxMachines,
    deleteAfterIdleMinutes: source.deleteAfterIdleMinutes,
    machineCpu: source.machineCpu,
    machineMemoryGb: source.machineMemoryGb,
    providerOptions: { ...source.providerOptions },
    env: envOverlayFromRows(source.envRows) ?? null,
    secretEnv: secretEnvOverlayFromRows(source.secretEnvRows) ?? null,
  }
}

function isWireObject(value: WireValue): value is WireObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function deepEqual(a: WireValue, b: WireValue, depth = 0): boolean {
  if (Object.is(a, b)) return true
  if (depth > 64) return false
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false
    return a.every((item, index) => deepEqual(item, b[index], depth + 1))
  }
  if (!isWireObject(a) || !isWireObject(b)) return false
  const keys = Object.keys(a)
  if (keys.length !== Object.keys(b).length) return false
  return keys.every((key) => key in b && deepEqual(a[key], b[key], depth + 1))
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
  const envOverlay = envOverlayFromRows(source.envRows)
  if (envOverlay) wire.env_overlay = envOverlay
  const secretEnvOverlay = secretEnvOverlayFromRows(source.secretEnvRows)
  if (secretEnvOverlay) wire.secret_env_overlay = secretEnvOverlay
}

export function toolWire(tool: BasicTool): ToolEntry {
  const wire: ToolEntry = tool.name.startsWith('app__') ? {} : { type: 'built_in' }
  if (tool.enabled === false) wire.enabled = false
  if (tool.permission != null) wire.permission = permissionWire(tool.permission)
  if (tool.deferred) wire.deferred = true
  return wire
}
