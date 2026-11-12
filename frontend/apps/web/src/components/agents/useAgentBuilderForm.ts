import type { AgentConfigDefinition, AgentConfigDefinitionInteractionHandler } from '@omnara/sdk'
import { useMemo, useState } from 'react'

import { type BasicMemoryStore } from '@/components/agents/agentConfigBasicExtract'
import { type AdvancedConfigEntry } from '@/components/agents/agentConfigCompatibility'
import {
  type BasicMcpServer,
  type BasicMcpTool,
  type McpAuthType,
  mcpServerNamesUnique,
  mcpServerValid,
} from '@/components/agents/agentConfigMcp'
import type { ModelSelection } from '@/components/agents/AgentConfigModelField'
import type { BasicConfigSession } from '@/components/agents/agentConfigSession'
import {
  type BasicSubagent,
  maxDepthValid,
  subagentsValid,
} from '@/components/agents/agentConfigSubagents'
import type { BasicTool } from '@/components/agents/AgentConfigToolsField'
import { useAgentBuilderTools } from '@/components/agents/useAgentBuilderTools'
import {
  type SecretRow,
  secretRowsValid,
  type TextRow,
  textRowsValid,
} from '@/components/key-value/keyValueRows'
import {
  emptyProviderOptions,
  optionalIdleDeletionMinutesValid,
  optionalPositiveInt32Valid,
  type ProviderOptionsDraft,
} from '@/components/machines/machineOverrides'
import { memoryGbDraftValid } from '@/lib/machine-memory'
import { resourceNameValid } from '@/lib/resource-name'

export { createBasicConfigSession } from '@/components/agents/agentConfigSession'
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
  sourceIndex?: number
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
  envRows: TextRow[]
  secretEnvRows: SecretRow[]
}

export interface BasicConfig {
  originalSource?: string
  savedSource?: string
  advancedEntries?: AdvancedConfigEntry[]
  instruction: string
  providerConfig: string
  modelName: string
  reasoningEffort: string
  machineSources: BasicMachineSource[]
  tools: BasicTool[]
  interactionHandlers: Record<string, AgentConfigDefinitionInteractionHandler>
  gitCredentials: AgentConfigDefinition['git_credentials']
  mcpServers: BasicMcpServer[]
  skillIds: string[]
  memoryStores: BasicMemoryStore[]
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
  reasoningEffort: '',
  machineSources: [],
  tools: [],
  interactionHandlers: {},
  gitCredentials: undefined,
  mcpServers: [],
  skillIds: [],
  memoryStores: [],
  subagents: [],
  maxSubagents: '',
  maxDepth: '',
}

export type AgentBuilderForm = ReturnType<typeof useAgentBuilderForm>

export function useAgentBuilderForm(
  session: BasicConfigSession,
  seedConfig: BasicConfig | undefined,
  scope: { orgId: string; projectId: string },
) {
  const [draft, setDraft] = useState<BasicConfig>(
    seedConfig
      ? {
          ...seedConfig,
          advancedEntries: seedConfig.advancedEntries ?? session.initialDraft?.advancedEntries,
        }
      : (session.initialDraft ?? emptyBasicConfig),
  )
  const [initialDraft] = useState(draft)
  const tools = useAgentBuilderTools(draft, scope)
  const [unavailableSkillIds, setUnavailableSkillIds] = useState<string[]>([])
  const [unavailableSourceIds, setUnavailableSourceIds] = useState<string[]>([])
  const [modelUnavailable, setModelUnavailable] = useState(false)

  const evaluated = useMemo(() => session.evaluate(draft), [session, draft])
  const initialYaml = useMemo(
    () => session.apply(session.initialDraft ?? initialDraft),
    [session, initialDraft],
  )
  const validationIssues = evaluated.issues
  const blocked =
    validationIssues.length > 0 ||
    unavailableSkillIds.length > 0 ||
    unavailableSourceIds.length > 0 ||
    modelUnavailable ||
    !basicConfigValid(draft)

  const patch = (fields: Partial<BasicConfig>) => {
    setDraft((prev) => ({ ...prev, ...fields }))
  }

  return {
    draft,
    sourceFormat: session.sourceFormat,
    dirty:
      evaluated.yaml !== initialYaml ||
      (validationIssues.length > 0 &&
        draftFingerprint(draft) !== draftFingerprint(session.initialDraft ?? initialDraft)),
    validationIssues,
    unappliedChanges: evaluated.unappliedChanges,
    advancedEntries: draft.advancedEntries ?? [],
    reservedToolNames: (draft.advancedEntries ?? [])
      .filter((entry) => !entry.removed && entry.path[0] === 'tools')
      .map((entry) => entry.path[1] ?? ''),
    isManaged: (key: string) =>
      !(draft.advancedEntries ?? []).some(
        (entry) => entry.path.length === 1 && entry.path[0] === key,
      ),
    setAdvancedEntries: (advancedEntries: AdvancedConfigEntry[]) => {
      patch({ advancedEntries })
    },
    evaluateAdvancedEntries: (advancedEntries: AdvancedConfigEntry[]) =>
      session.evaluate({
        ...(session.initialDraft ?? emptyBasicConfig),
        tools: draft.tools,
        advancedEntries,
      }),
    yaml: evaluated.yaml,
    blocked,
    resolvedTools: tools.data?.tools,
    toolsPending: tools.isPending,
    toolsError: tools.isError,
    toolsErrorMessage: tools.errorMessage,
    retryTools: () => void tools.refetch(),
    reset: (config: BasicConfig | null) => {
      setDraft((previous) => ({
        ...(config ?? emptyBasicConfig),
        savedSource: previous.savedSource,
      }))
    },
    instruction: draft.instruction,
    model: {
      providerConfig: draft.providerConfig,
      modelName: draft.modelName,
      reasoningEffort: draft.reasoningEffort,
    },
    machineSources: draft.machineSources,
    tools: draft.tools,
    interactionHandlers: draft.interactionHandlers,
    gitCredentials: draft.gitCredentials,
    skillIds: draft.skillIds,
    memoryStores: draft.memoryStores,
    mcpServers: draft.mcpServers,
    subagents: draft.subagents,
    maxSubagents: draft.maxSubagents,
    maxDepth: draft.maxDepth,
    setInstruction: (instruction: string) => {
      patch({ instruction })
    },
    setModel: (model: ModelSelection) => {
      patch({
        providerConfig: model.providerConfig,
        modelName: model.modelName,
        reasoningEffort: model.reasoningEffort,
      })
    },
    setMachineSources: (machineSources: BasicMachineSource[]) => {
      patch({ machineSources })
    },
    setTools: (tools: BasicTool[]) => {
      patch({ tools })
    },
    setGitCredentials: (gitCredentials: BasicConfig['gitCredentials']) => {
      patch({ gitCredentials })
    },
    setSkillIds: (skillIds: string[]) => {
      patch({ skillIds })
    },
    setMemoryStores: (memoryStores: BasicMemoryStore[]) => {
      patch({ memoryStores })
    },
    setMcpServers: (mcpServers: BasicMcpServer[]) => {
      patch({ mcpServers })
    },
    setSubagents: (subagents: BasicSubagent[]) => {
      patch(
        subagents.length === 0 &&
          !(draft.advancedEntries ?? []).some((entry) => entry.path[0] === 'subagents')
          ? {
              subagents,
              maxSubagents: '',
              maxDepth: maxDepthValid(draft.maxDepth) ? draft.maxDepth : '',
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
  const preservedSubagents = (draft.advancedEntries ?? []).some(
    (entry) => entry.path[0] === 'subagents',
  )
  return (
    ((draft.advancedEntries ?? []).some(
      (entry) => entry.path.length === 1 && entry.path[0] === 'instruction',
    ) ||
      draft.instruction.trim() !== '') &&
    ((draft.advancedEntries ?? []).some(
      (entry) => entry.path.length === 1 && entry.path[0] === 'model',
    ) ||
      (resourceNameValid(draft.providerConfig) && resourceNameValid(draft.modelName))) &&
    draft.machineSources.every(machineSourceValid) &&
    mcpServerNamesUnique(draft.mcpServers) &&
    draft.mcpServers.every(mcpServerValid) &&
    optionalPositiveInt32Valid(draft.maxSubagents) &&
    subagentsValid(draft.subagents, preservedSubagents ? '' : draft.maxSubagents, draft.maxDepth)
  )
}

const nonNegativeIntegerPattern = /^(0|[1-9][0-9]*)$/

function machineCountValid(value: string) {
  return value === '' || nonNegativeIntegerPattern.test(value)
}

function machineSourceValid(source: BasicMachineSource) {
  return (
    resourceNameValid(source.name) &&
    textRowsValid(source.envRows) &&
    secretRowsValid(source.secretEnvRows) &&
    (source.kind === 'machine' ||
      (machineCountValid(source.initialNumMachines) &&
        machineCountValid(source.maxMachines) &&
        optionalIdleDeletionMinutesValid(source.deleteAfterIdleMinutes) &&
        optionalPositiveInt32Valid(source.machineCpu) &&
        memoryGbDraftValid(source.machineMemoryGb, { optional: true })))
  )
}

function draftFingerprint(draft: BasicConfig) {
  return JSON.stringify({ ...draft, originalSource: undefined, savedSource: undefined })
}
