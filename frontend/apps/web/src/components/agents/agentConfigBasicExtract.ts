import type { ToolPermissionSelection } from '@omnara/sdk'
import { z } from 'zod'

import type {
  BasicConfig,
  BasicMachineSource,
  BasicMcpServer,
  BasicMcpTool,
  BasicSubagent,
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

const permission = z.strictObject({
  mode: z.string(),
  parameters: z.record(z.string(), z.unknown()).optional(),
})

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
})

const mcpEntry = z.strictObject({
  url: z.string(),
  permission: permission.optional(),
  default_enabled: z.boolean().nullable().optional(),
  auth: mcpAuth.optional(),
  tools: z.record(z.string(), mcpToolEntry).optional(),
})

const toolEntry = z.strictObject({
  type: z.literal('built_in').optional(),
  enabled: z.literal(true).nullable().optional(),
  permission: permission.optional(),
})

const optionalText = z.string().nullable().optional()

const subagentEntry = z.looseObject({
  type: z.enum(['profile', 'self']),
  profile: z.string().optional(),
  description: z.string().optional(),
  model: z.record(z.string(), z.unknown()).optional(),
  instruction: z.strictObject({ append: z.string().optional() }).optional(),
  max_concurrent: positiveCount,
  archive_after_idle_minutes: positiveCount,
})

const basicDocument = z.looseObject({
  version: z.literal('v1').optional(),
  instruction: optionalText,
  model: z.looseObject({ provider_config: optionalText, name: optionalText }).nullable().optional(),
  machine_sources: z.array(z.union([poolEntry, machineEntry])).optional(),
  tools: z.record(z.string(), toolEntry).optional(),
  skills: z.array(z.string()).optional(),
  mcp: z.record(z.string(), mcpEntry).optional(),
  subagents: z.record(z.string(), subagentEntry).optional(),
  max_subagents: positiveCount,
})

export function extractBasicConfig(js: unknown): BasicConfig | null {
  const parsed = basicDocument.safeParse(js)
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
    tools: Object.entries(doc.tools ?? {}).map(([name, entry]) => ({
      name,
      permission: permissionDraft(entry.permission),
    })),
    mcpServers: Object.entries(doc.mcp ?? {}).map(([name, entry]) => mcpServerDraft(name, entry)),
    skillIds: doc.skills ?? [],
    subagents: Object.entries(doc.subagents ?? {}).map(([handle, entry]) =>
      subagentDraft(handle, entry),
    ),
    maxSubagents: countDraft(doc.max_subagents),
  }
}

function subagentDraft(handle: string, entry: z.infer<typeof subagentEntry>): BasicSubagent {
  return {
    id: crypto.randomUUID(),
    handle,
    type: entry.type,
    profileName: entry.profile ?? '',
    description: entry.description ?? '',
    instructionAppend: normalizeMultiline(entry.instruction?.append ?? ''),
    maxConcurrent: countDraft(entry.max_concurrent),
    archiveAfterIdleMinutes: countDraft(entry.archive_after_idle_minutes),
    modelOverride: entry.model,
  }
}

export function normalizeMultiline(value: string) {
  return value.replace(/\r\n?/g, '\n').trimEnd()
}

function permissionDraft(
  value: z.infer<typeof permission> | undefined,
): ToolPermissionSelection | null {
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
  return {
    id: crypto.randomUUID(),
    name,
    url: entry.url,
    permission: permissionDraft(entry.permission),
    defaultEnabled: entry.default_enabled ?? true,
    authType: auth?.type ?? 'none',
    secretId: auth?.secret_id ?? '',
    service: auth?.type === 'sigv4' ? auth.service : '',
    region: auth?.type === 'sigv4' ? auth.region : '',
    tools: Object.entries(entry.tools ?? {}).map(
      ([name, tool]): BasicMcpTool => ({
        name,
        enabled: tool.enabled ?? null,
        permission: permissionDraft(tool.permission),
      }),
    ),
  }
}
