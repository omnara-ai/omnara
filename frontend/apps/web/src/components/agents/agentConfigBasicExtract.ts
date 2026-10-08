import type { ToolPermissionSelection } from '@omnara/sdk'
import {
  zAgentConfigDefinition,
  zAgentConfigDefinitionEventWebhook,
  zAgentConfigDefinitionInteractionHandler,
  zAgentConfigDefinitionMachineSource,
  zAgentConfigDefinitionMcpAuth,
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
import { type SecretRow, type TextRow } from '@/components/key-value/keyValueRows'
import {
  emptyProviderOptions,
  type ProviderOptionsDraft,
} from '@/components/machines/machineOverrides'
import { machinePoolProviderDefinitions } from '@/components/org/machinePoolProviders'
import { memoryGbDraft } from '@/lib/machine-memory'

// These schemas reuse the generated AgentConfigDefinition schemas, so their
// fields and value rules follow the OpenAPI spec. Where they differ, it is for
// the builder: every object is strict, so a config with fields the builder
// cannot show stays in YAML mode rather than losing them; some entries are
// narrowed to what the builder can edit; and unfinished drafts are accepted.

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

// The builder writes rows as soon as they are added, before their names, URLs,
// and secrets are filled in, so references it edits accept any text here and
// the form and server validate them instead.
const draftText = z.string()
const draftSecretOverlay = z.record(z.string(), draftText.nullable()).nullish()

// The generated machine source intersects "exactly one of machine_name or
// machine_pool_name" with the fields; the builder splits the two kinds itself.
const machineSourceFields = zAgentConfigDefinitionMachineSource.def.right.shape
// The builder edits provider options as text fields.
const providerOptionsOverlay = z.record(z.string(), z.string()).optional()

const machineEntry = z.strictObject({
  machine_name: draftText,
  cwd: machineSourceFields.cwd,
  env_overlay: machineSourceFields.env_overlay,
  secret_env_overlay: draftSecretOverlay,
})

export type MachineEntry = z.infer<typeof machineEntry>

const poolEntry = z.strictObject({
  machine_pool_name: draftText,
  initial_num_machines: machineSourceFields.initial_num_machines,
  max_machines: machineSourceFields.max_machines,
  delete_after_idle_minutes: machineSourceFields.delete_after_idle_minutes,
  machine_cpu: machineSourceFields.machine_cpu,
  machine_memory_mb: machineSourceFields.machine_memory_mb,
  machine_provider_options_overlay: providerOptionsOverlay,
  cwd: machineSourceFields.cwd,
  env_overlay: machineSourceFields.env_overlay,
  secret_env_overlay: draftSecretOverlay,
})

export type PoolEntry = z.infer<typeof poolEntry>

const authFields = zAgentConfigDefinitionMcpAuth.shape
// The builder's SigV4 form requires a service and region.
const mcpAuth = z.discriminatedUnion('type', [
  z.strictObject({
    type: authFields.type.extract(['oauth', 'bearer']),
    secret_id: draftText,
  }),
  z.strictObject({
    type: authFields.type.extract(['sigv4']),
    secret_id: draftText,
    service: draftText,
    region: draftText,
  }),
])

const mcpToolEntry = z.strictObject({
  ...zAgentConfigDefinitionMcpTool.shape,
  permission: permission.optional(),
})

export type McpToolEntry = z.infer<typeof mcpToolEntry>

const mcpEntry = z.strictObject({
  ...zAgentConfigDefinitionMcpServer.shape,
  url: draftText,
  permission: permission.optional(),
  auth: mcpAuth.optional(),
  tools: z.record(z.string(), mcpToolEntry).optional(),
})

export type McpEntry = z.infer<typeof mcpEntry>

// The builder shows built-in tools only; custom tools stay in YAML mode.
const toolEntry = z.strictObject({
  type: zAgentConfigDefinitionTool.shape.type.unwrap().extract(['built_in']).optional(),
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
  // The spec's dependentRequired rule, which the generated schema omits.
  .refine((model) => (model.provider_config === undefined) === (model.name === undefined), {
    message: 'Model provider_config and name must be provided together.',
  })
export type SubagentModelEntry = z.infer<typeof subagentModelEntry>

const subagentEntry = z.strictObject({
  ...zAgentConfigDefinitionSubagent.shape,
  profile: draftText.optional(),
  model: subagentModelEntry.optional(),
  instruction: z.strictObject(zAgentConfigDefinitionSubagentInstruction.shape).optional(),
})
export type SubagentEntry = z.infer<typeof subagentEntry>

const memoryStoreEntry = z.strictObject({
  ...zAgentConfigDefinitionMemoryStore.shape,
  name: draftText,
})
export type BasicMemoryStore = z.infer<typeof memoryStoreEntry>

const definitionFields = zAgentConfigDefinition.shape
const webhookFields = zAgentConfigDefinitionEventWebhook.shape
const optionalText = z.string().nullable().optional()

// The document itself stays loose and accepts unfinished drafts, such as an
// empty instruction or a model without a name yet.
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
    .nullable()
    .optional(),
  machine_sources: z.array(z.union([poolEntry, machineEntry])).optional(),
  tools: z.record(z.string(), toolEntry).optional(),
  interaction_handlers: z.record(z.string(), zAgentConfigDefinitionInteractionHandler).nullish(),
  skills: definitionFields.skills,
  memory_stores: z.array(memoryStoreEntry).optional(),
  mcp: z.record(z.string(), mcpEntry).optional(),
  event_webhook: z
    .strictObject({
      ...webhookFields,
      // A webhook being set up in the builder may have no events selected yet.
      events: z.array(webhookFields.events.element).optional(),
    })
    .optional(),
  subagents: z.record(z.string(), subagentEntry).optional(),
  max_subagents: definitionFields.max_subagents,
  max_depth: definitionFields.max_depth,
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
    reasoningEffort: doc.model?.reasoning?.effort ?? '',
    machineSources,
    tools: Object.entries(doc.tools ?? {}).map(([name, entry]) => toolDraft(name, entry)),
    interactionHandlers: doc.interaction_handlers ?? {},
    mcpServers: Object.entries(doc.mcp ?? {}).map(([name, entry]) => mcpServerDraft(name, entry)),
    eventWebhookEvents: doc.event_webhook ? (doc.event_webhook.events ?? []) : ['tool_call_update'],
    eventWebhookUrl: doc.event_webhook?.url ?? '',
    eventWebhookSigningSecretId: doc.event_webhook?.signing_secret_id ?? '',
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
      ([key, value]): TextRow => ({ id: crypto.randomUUID(), key, value }),
    ),
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
