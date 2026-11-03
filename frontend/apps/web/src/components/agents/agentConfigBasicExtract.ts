import type { ToolPermissionSelection } from '@omnara/sdk'
import {
  zAgentConfigDefinition,
  zAgentConfigDefinitionGitCredentials,
  zAgentConfigDefinitionInteractionHandler,
  zAgentConfigDefinitionMcpServer,
  zAgentConfigDefinitionMcpTool,
  zAgentConfigDefinitionMemoryStore,
  zAgentConfigDefinitionModel,
  zAgentConfigDefinitionSubagent,
  zAgentConfigDefinitionSubagentInstruction,
  zAgentConfigDefinitionSubagentModel,
  zAgentConfigDefinitionTool,
  zAgentConfigDefinitionToolPermission,
} from '@omnara/sdk/zod'
import { Document, isMap, isNode } from 'yaml'
import { z } from 'zod'

import {
  type AdvancedConfigEntry,
  type ConfigDiagnostic,
  parseConfigDocument,
  validateAdvancedEntry,
} from '@/components/agents/agentConfigCompatibility'
import type { BasicSubagent } from '@/components/agents/agentConfigSubagents'
import type { BasicTool } from '@/components/agents/AgentConfigToolsField'
import type {
  BasicConfig,
  BasicMachineSource,
  BasicMcpServer,
  BasicMcpTool,
} from '@/components/agents/useAgentBuilderForm'
import { type SecretRow, type TextRow } from '@/components/key-value/keyValueRows'
import {
  emptyProviderOptions,
  idleDeletionMinutesValid,
  type ProviderOptionsDraft,
} from '@/components/machines/machineOverrides'
import { machinePoolProviderDefinitions } from '@/components/org/machinePoolProviders'
import { memoryGbDraft } from '@/lib/machine-memory'

// Strict projections identify the paths the visual controls own; other paths stay in the AST.

const permissionParameters = z.record(z.string(), z.json())

const permission = z.strictObject({
  ...zAgentConfigDefinitionToolPermission.shape,
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
const overlay = z.record(z.string(), z.string().nullable()).nullish()
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
  z.strictObject({ type: z.enum(['oauth', 'bearer']), secret_id: z.string() }),
  z.strictObject({
    type: z.literal('sigv4'),
    secret_id: z.string(),
    service: z.string(),
    region: z.string(),
  }),
])

const mcpToolEntry = z.strictObject({
  ...zAgentConfigDefinitionMcpTool.shape,
  permission: permission.optional(),
})

export type McpToolEntry = z.infer<typeof mcpToolEntry>

const mcpEntry = z.strictObject({
  url: z.string(),
  permission: permission.optional(),
  default_enabled: z.boolean().nullish(),
  deferred: z.boolean().optional(),
  auth: mcpAuth.optional(),
  tools: z.record(z.string(), mcpToolEntry).optional(),
})

export type McpEntry = z.infer<typeof mcpEntry>

const toolEntry = z.strictObject({
  type: z.literal('built_in').optional(),
  enabled: zAgentConfigDefinitionTool.shape.enabled,
  permission: permission.optional(),
  deferred: zAgentConfigDefinitionTool.shape.deferred,
})

export type ToolEntry = z.infer<typeof toolEntry>

const subagentModelFields = zAgentConfigDefinitionSubagentModel.shape
const subagentModelEntry = z
  .strictObject({
    ...subagentModelFields,
    reasoning: z.strictObject(subagentModelFields.reasoning.unwrap().shape).optional(),
  })
  .refine((model) => (model.provider_config === undefined) === (model.name === undefined), {
    message: 'Model provider_config and name must be provided together.',
  })
export type SubagentModelEntry = z.infer<typeof subagentModelEntry>

const subagentEntry = z.strictObject({
  ...zAgentConfigDefinitionSubagent.shape,
  profile: z.string().optional(),
  model: subagentModelEntry.optional(),
  instruction: z.strictObject(zAgentConfigDefinitionSubagentInstruction.shape).optional(),
})
export type SubagentEntry = z.infer<typeof subagentEntry>

const memoryStoreEntry = z.strictObject({
  ...zAgentConfigDefinitionMemoryStore.shape,
  name: z.string(),
})
export type BasicMemoryStore = z.infer<typeof memoryStoreEntry>

const definitionFields = zAgentConfigDefinition.shape
const optionalText = z.string().nullish()

const basicDocument = z.looseObject({
  version: definitionFields.version,
  instruction: optionalText,
  model: z
    .looseObject({
      provider_config: optionalText,
      name: optionalText,
      reasoning: z
        .strictObject(zAgentConfigDefinitionModel.shape.reasoning.unwrap().shape)
        .optional(),
    })
    .nullish(),
  machine_sources: z.array(z.union([poolEntry, machineEntry])).nullish(),
  tools: z.record(z.string(), toolEntry).nullish(),
  interaction_handlers: z.record(z.string(), zAgentConfigDefinitionInteractionHandler).nullish(),
  git_credentials: z.strictObject(zAgentConfigDefinitionGitCredentials.shape).nullish(),
  skills: definitionFields.skills,
  memory_stores: z.array(memoryStoreEntry).nullish(),
  mcp: z.record(z.string(), mcpEntry).nullish(),
  subagents: z.record(z.string(), subagentEntry).nullish(),
  max_subagents: definitionFields.max_subagents,
  // Out-of-range depths still load so the form can flag them; maxDepthValid enforces the limit.
  max_depth: positiveCount,
})

export function parseBasicConfigSource(source: string) {
  const { doc, diagnostics } = parseConfigDocument(source)
  if (!isMap(doc.contents) && diagnostics.length === 0) {
    diagnostics.push({
      path: [],
      kind: 'fatal',
      error: true,
      message: 'The configuration must be a YAML or JSON mapping.',
    })
  }
  if (diagnostics.length > 0) return { doc, initialDraft: null, diagnostics, advancedEntries: [] }
  const advancedEntries: AdvancedConfigEntry[] = []
  const rawResult = z.record(z.string(), z.json()).safeParse(doc.toJS())
  if (!rawResult.success) {
    diagnostics.push(
      ...rawResult.error.issues.map((issue): ConfigDiagnostic => ({
        path: issue.path.map(String),
        kind: 'fatal',
        error: true,
        message: 'This value cannot be represented as JSON configuration: ' + issue.message,
      })),
    )
    return { doc, initialDraft: null, diagnostics, advancedEntries }
  }
  const raw = rawResult.data
  const projection: typeof raw = {}
  const preserve = (path: string[], kind: AdvancedConfigEntry['kind'] = 'opaque') => {
    const node = doc.getIn(path, true)
    // Render a standalone editor value. The original node remains the source of truth until edited.
    const fragment = new Document()
    fragment.contents = isNode(node) ? node : null
    const entry = { path, kind, source: fragment.toString() }
    advancedEntries.push(entry)
    diagnostics.push({
      path,
      kind: kind === 'custom' ? 'advanced' : 'opaque',
      message:
        kind === 'custom'
          ? 'Custom tool definition preserved. Edit its description and schema in Advanced YAML.'
          : 'Preserved without changes. Edit this definition in YAML.',
    })
    if (kind === 'custom') diagnostics.push(...validateAdvancedEntry(entry))
  }
  const invalid = (path: string[], result: z.ZodSafeParseResult<unknown>) => {
    if (result.success) return
    diagnostics.push(
      ...result.error.issues
        .filter((issue) => issue.code !== 'unrecognized_keys')
        .map((issue): ConfigDiagnostic => ({
          path: [...path, ...issue.path.map(String)],
          kind: 'opaque',
          error: true,
          message: issue.message,
        })),
    )
  }
  const authoritativeEntries = new Map<string, z.ZodType>([
    ['tools', zAgentConfigDefinitionTool],
    ['mcp', zAgentConfigDefinitionMcpServer],
    ['subagents', zAgentConfigDefinitionSubagent],
    ['interaction_handlers', zAgentConfigDefinitionInteractionHandler],
  ])
  const namedSchemas = new Map<string, z.ZodType>([
    ['tools', toolEntry],
    ['mcp', mcpEntry],
    ['subagents', subagentEntry],
    ['interaction_handlers', zAgentConfigDefinitionInteractionHandler],
  ])
  for (const [key, value] of Object.entries(raw)) {
    const namedSchema = namedSchemas.get(key)
    const entries = z.record(z.string(), z.json()).safeParse(value)
    if (namedSchema && entries.success) {
      const managed: typeof raw = {}
      for (const [name, entry] of Object.entries(entries.data)) {
        if (key === 'tools' && z.object({ type: z.literal('custom') }).safeParse(entry).success) {
          preserve([key, name], 'custom')
        } else if (namedSchema.safeParse(entry).success) managed[name] = entry
        else {
          preserve([key, name])
          const authoritative = authoritativeEntries.get(key)
          if (authoritative) invalid([key, name], authoritative.safeParse(entry))
          // The generated schema omits the conditional provider/name requirement.
          const projected = namedSchema.safeParse(entry)
          if (!projected.success)
            diagnostics.push(
              ...projected.error.issues
                .filter((issue) => issue.code === 'custom')
                .map((issue): ConfigDiagnostic => ({
                  path: [key, name, ...issue.path.map(String)],
                  kind: 'opaque',
                  error: true,
                  message: issue.message,
                })),
            )
        }
      }
      projection[key] = managed
      continue
    }
    const field = Object.entries(basicDocument.shape).find(([name]) => name === key)?.[1]
    if (field?.safeParse(value).success) projection[key] = value
    else {
      preserve([key])
      const authoritative = Object.entries(definitionFields).find(([name]) => name === key)?.[1]
      if (authoritative) invalid([key], authoritative.safeParse(value))
    }
  }
  // A provider-specific overlay may be valid without having visual controls for its keys.
  const parsed = basicDocument.parse(projection)
  const machineSources: BasicMachineSource[] = []
  for (const entry of parsed.machine_sources ?? []) {
    const source = machineSourceDraft(entry)
    if (source == null) {
      preserve(['machine_sources'])
      parsed.machine_sources = []
      machineSources.length = 0
      break
    }
    machineSources.push(source)
  }
  // Model extensions are preserved by leaf edits, but still explain their ownership.
  const model = z.record(z.string(), z.unknown()).safeParse(raw.model)
  if (model.success) {
    for (const key of Object.keys(model.data)) {
      if (!['provider_config', 'name', 'reasoning'].includes(key)) {
        diagnostics.push({
          path: ['model', key],
          kind: 'opaque',
          message: 'Preserved without changes. Edit this field in YAML.',
        })
      }
    }
  }
  return {
    doc,
    initialDraft: {
      ...extractBasicConfig(parsed, machineSources, advancedEntries),
      originalSource: source,
    },
    diagnostics,
    advancedEntries,
  }
}

function extractBasicConfig(
  doc: z.infer<typeof basicDocument>,
  machineSources: BasicMachineSource[],
  advancedEntries: AdvancedConfigEntry[],
): BasicConfig {
  return {
    advancedEntries,
    instruction: normalizeMultiline(doc.instruction ?? ''),
    providerConfig: doc.model?.provider_config ?? '',
    modelName: doc.model?.name ?? '',
    reasoningEffort: doc.model?.reasoning?.effort ?? '',
    machineSources,
    tools: Object.entries(doc.tools ?? {}).map(([name, entry]) => toolDraft(name, entry)),
    interactionHandlers: doc.interaction_handlers ?? {},
    gitCredentials: doc.git_credentials ?? undefined,
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
    envRows: Object.entries(entry.env_overlay ?? {}).map(([key, value]): TextRow => ({
      id: crypto.randomUUID(),
      key,
      value,
    })),
    secretEnvRows: Object.entries(entry.secret_env_overlay ?? {}).map(
      ([key, secretId]): SecretRow => ({ id: crypto.randomUUID(), key, secretId }),
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
      else if (key === definition.location?.key) options.location = entry
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
