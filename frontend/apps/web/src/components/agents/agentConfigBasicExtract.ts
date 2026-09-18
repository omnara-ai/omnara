import type { ToolPermissionSelection } from '@omnara/sdk'
import { type Document, isAlias, isScalar, visit } from 'yaml'
import { z } from 'zod'

import type { BasicSubagent } from '@/components/agents/agentConfigSubagents'
import type { BasicTool } from '@/components/agents/AgentConfigToolsField'
import type {
  BasicConfig,
  BasicMachineSource,
  BasicMcpServer,
  BasicMcpTool,
} from '@/components/agents/useAgentBuilderForm'
import {
  emptyProviderOptions,
  type EnvOverlayRow,
  idleDeletionMinutesValid,
  type ProviderOptionsDraft,
  type SecretEnvOverlayRow,
} from '@/components/machines/machineOverrides'
import { machinePoolProviderDefinitions } from '@/components/org/machinePoolProviders'
import { memoryGbDraft } from '@/lib/machine-memory'

const permissionParameters = z.record(z.string(), z.json())

const permission = z.strictObject({
  mode: z.string(),
  parameters: permissionParameters.optional(),
})

export type PermissionEntry = z.infer<typeof permission>

export interface PermissionSelection {
  mode: PermissionEntry['mode']
  parameters: z.output<typeof permissionParameters>
}

export function permissionSelection(selection: ToolPermissionSelection): PermissionSelection {
  return { mode: selection.mode, parameters: permissionParameters.parse(selection.parameters) }
}

const positiveCount = z.number().int().positive().optional()
const nonNegativeCount = z.number().int().nonnegative().optional()
const idleDeletionMinutes = z.number().refine(idleDeletionMinutesValid).optional()
const overlay = z.record(z.string(), z.string().nullable()).optional()
const providerOptionsOverlay = z.record(z.string(), z.string()).optional()

const machineEntry = z.strictObject({
  machine_name: z.string(),
  cwd: z.string().optional(),
  env_overlay: overlay,
  secret_env_overlay: overlay,
})

export type MachineEntry = z.infer<typeof machineEntry>

const poolEntry = z.strictObject({
  machine_pool_name: z.string(),
  initial_num_machines: nonNegativeCount,
  max_machines: nonNegativeCount,
  delete_after_idle_minutes: idleDeletionMinutes,
  machine_cpu: positiveCount,
  machine_memory_mb: positiveCount,
  machine_provider_options_overlay: providerOptionsOverlay,
  cwd: z.string().optional(),
  env_overlay: overlay,
  secret_env_overlay: overlay,
})

export type PoolEntry = z.infer<typeof poolEntry>

const mcpAuth = z.discriminatedUnion('type', [
  z.strictObject({ type: z.literal('oauth'), secret_id: z.string() }),
  z.strictObject({ type: z.literal('bearer'), secret_id: z.string() }),
  z.strictObject({
    type: z.literal('sigv4'),
    secret_id: z.string(),
    service: z.string(),
    region: z.string(),
  }),
])

const mcpToolEntry = z.strictObject({
  enabled: z.boolean().nullable().optional(),
  permission: permission.optional(),
  deferred: z.boolean().nullable().optional(),
})

export type McpToolEntry = z.infer<typeof mcpToolEntry>

const mcpEntry = z.strictObject({
  url: z.string(),
  permission: permission.optional(),
  default_enabled: z.boolean().nullable().optional(),
  deferred: z.boolean().optional(),
  auth: mcpAuth.optional(),
  tools: z.record(z.string(), mcpToolEntry).optional(),
})

export type McpEntry = z.infer<typeof mcpEntry>

const toolEntry = z.strictObject({
  type: z.literal('built_in').optional(),
  enabled: z.boolean().nullable().optional(),
  permission: permission.optional(),
  deferred: z.boolean().optional(),
})

export type ToolEntry = z.infer<typeof toolEntry>

const optionalText = z.string().nullable().optional()

const subagentModelEntry = z.strictObject({
  provider_config: z.string().optional(),
  name: z.string().optional(),
  context_window_tokens: positiveCount,
  default_max_output_tokens: positiveCount,
  cache_retention: z.string().optional(),
  reasoning: z.strictObject({ effort: z.string() }).optional(),
})
export type SubagentModelEntry = z.infer<typeof subagentModelEntry>

const subagentEntry = z.strictObject({
  type: z.enum(['profile', 'self']),
  profile: z.string().optional(),
  description: z.string().optional(),
  model: subagentModelEntry.optional(),
  instruction: z.strictObject({ append: z.string().optional() }).optional(),
  max_instances: positiveCount,
  archive_after_idle_minutes: positiveCount,
})
export type SubagentEntry = z.infer<typeof subagentEntry>

const memoryStoreEntry = z.strictObject({
  name: z.string(),
  access: z.enum(['read_only', 'read_write']),
})
export type BasicMemoryStore = z.infer<typeof memoryStoreEntry>

const basicDocument = z.looseObject({
  version: z.literal('v1').optional(),
  instruction: optionalText,
  model: z.looseObject({ provider_config: optionalText, name: optionalText }).nullable().optional(),
  machine_sources: z.array(z.union([poolEntry, machineEntry])).optional(),
  tools: z.record(z.string(), toolEntry).optional(),
  skills: z.array(z.string()).optional(),
  memory_stores: z.array(memoryStoreEntry).optional(),
  mcp: z.record(z.string(), mcpEntry).optional(),
  subagents: z.record(z.string(), subagentEntry).optional(),
  max_subagents: positiveCount,
  max_depth: positiveCount,
})

export function extractBasicConfig(document: Document): BasicConfig | null {
  const sharedYaml = { found: false }
  visit(document, {
    Node(key, node) {
      if (
        isAlias(node) ||
        node.anchor ||
        (key === 'key' &&
          isScalar(node) &&
          node.value === '<<' &&
          (node.type === 'PLAIN' || node.tag === 'tag:yaml.org,2002:merge'))
      ) {
        sharedYaml.found = true
        return visit.BREAK
      }
      return undefined
    },
  })
  if (sharedYaml.found) return null

  const parsed = basicDocument.safeParse(document.toJS())
  if (!parsed.success) return null
  const doc = parsed.data
  const machineSources: BasicMachineSource[] = []
  for (const entry of doc.machine_sources ?? []) {
    const source = machineSourceDraft(entry)
    if (source == null) return null
    machineSources.push(source)
  }

  return {
    instruction: normalizeMultiline(doc.instruction ?? ''),
    providerConfig: doc.model?.provider_config ?? '',
    modelName: doc.model?.name ?? '',
    machineSources,
    tools: Object.entries(doc.tools ?? {}).map(([name, entry]) => toolDraft(name, entry)),
    mcpServers: Object.entries(doc.mcp ?? {}).map(([name, entry]) => mcpServerDraft(name, entry)),
    skillIds: doc.skills ?? [],
    memoryStores: doc.memory_stores ?? [],
    subagents: Object.entries(doc.subagents ?? {}).map(([key, entry]) => subagentDraft(key, entry)),
    maxSubagents: countDraft(doc.max_subagents),
    maxDepth: countDraft(doc.max_depth),
  }
}

function subagentDraft(key: string, entry: z.infer<typeof subagentEntry>): BasicSubagent {
  return {
    id: crypto.randomUUID(),
    key,
    type: entry.type,
    profileName: entry.profile ?? '',
    description: entry.description ?? '',
    instructionAppend: normalizeMultiline(entry.instruction?.append ?? ''),
    maxInstances: countDraft(entry.max_instances),
    archiveAfterIdleMinutes: countDraft(entry.archive_after_idle_minutes),
    modelOverride: entry.model,
  }
}

export function normalizeMultiline(value: string) {
  return value.replace(/\r\n?/g, '\n').trimEnd()
}

function toolDraft(name: string, entry: z.infer<typeof toolEntry>): BasicTool {
  const draft: BasicTool = {
    name,
    enabled: entry.enabled ?? undefined,
    permission: permissionDraft(entry.permission),
  }
  if (entry.deferred) draft.deferred = true
  return draft
}

function permissionDraft(
  value: z.infer<typeof permission> | undefined,
): PermissionSelection | null {
  if (value == null) return null
  return { mode: value.mode, parameters: value.parameters ?? {} }
}

function machineSourceDraft(
  entry: z.infer<typeof poolEntry> | z.infer<typeof machineEntry>,
): BasicMachineSource | null {
  const isPool = 'machine_pool_name' in entry
  const providerOverlay = isPool
    ? inferProviderOverlay(entry.machine_provider_options_overlay)
    : { provider: '', options: emptyProviderOptions }
  if (providerOverlay == null) return null
  return {
    id: crypto.randomUUID(),
    kind: isPool ? 'pool' : 'machine',
    name: isPool ? entry.machine_pool_name : entry.machine_name,
    provider: providerOverlay.provider,
    managementKind: '',
    defaultCwd: entry.cwd ?? '',
    initialNumMachines: countDraft(isPool ? entry.initial_num_machines : undefined),
    maxMachines: countDraft(isPool ? entry.max_machines : undefined),
    deleteAfterIdleMinutes: countDraft(isPool ? entry.delete_after_idle_minutes : undefined),
    machineCpu: countDraft(isPool ? entry.machine_cpu : undefined),
    machineMemoryGb: memoryGbDraft(isPool ? entry.machine_memory_mb : undefined),
    providerOptions: providerOverlay.options,
    envRows: Object.entries(entry.env_overlay ?? {}).map(
      ([key, value]): EnvOverlayRow => ({ id: crypto.randomUUID(), key, value }),
    ),
    secretEnvRows: Object.entries(entry.secret_env_overlay ?? {}).map(
      ([key, secretId]): SecretEnvOverlayRow => ({ id: crypto.randomUUID(), key, secretId }),
    ),
  }
}

function inferProviderOverlay(
  value?: Record<string, string>,
): { provider: string; options: ProviderOptionsDraft } | null {
  if (value === undefined) return { provider: '', options: emptyProviderOptions }
  for (const [provider, definition] of Object.entries(machinePoolProviderDefinitions)) {
    const options = { ...emptyProviderOptions }
    let matched = true
    for (const [key, entry] of Object.entries(value)) {
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

function countDraft(value?: number): string {
  return value === undefined ? '' : String(value)
}

function mcpServerDraft(name: string, entry: z.infer<typeof mcpEntry>): BasicMcpServer {
  const auth = entry.auth
  const draft: BasicMcpServer = {
    id: crypto.randomUUID(),
    name,
    url: entry.url,
    permission: permissionDraft(entry.permission),
    defaultEnabled: entry.default_enabled ?? true,
    authType: auth?.type ?? 'none',
    secretId: auth?.secret_id ?? '',
    service: auth?.type === 'sigv4' ? auth.service : '',
    region: auth?.type === 'sigv4' ? auth.region : '',
    tools: Object.entries(entry.tools ?? {}).map(([name, tool]) => mcpToolDraft(name, tool)),
  }
  if (entry.deferred) draft.deferred = true
  return draft
}

function mcpToolDraft(name: string, tool: z.infer<typeof mcpToolEntry>): BasicMcpTool {
  const draft: BasicMcpTool = {
    name,
    enabled: tool.enabled ?? null,
    permission: permissionDraft(tool.permission),
  }
  if (tool.deferred != null) draft.deferred = tool.deferred
  return draft
}
