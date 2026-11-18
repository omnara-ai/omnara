import {
  normalizeMultiline,
  type SubagentEntry,
  type SubagentModelEntry,
} from '@/components/agents/agentConfigBasicExtract'
import type { ModelSelection } from '@/components/agents/AgentConfigModelField'
import { optionalPositiveInt32Valid } from '@/components/machines/machineOverrides'
import { normalizeResourceName, resourceNameError } from '@/lib/resource-name'

export type SubagentType = 'profile' | 'self'

export interface BasicSubagent {
  id: string
  key: string
  type: SubagentType
  profileName: string
  description: string
  instructionAppend: string
  maxInstances: string
  archiveAfterIdleMinutes: string
  modelOverride?: SubagentModelEntry
}

/** The model picker's view of a subagent's model override; empty fields mean no override. */
export function subagentModelSelection(override: SubagentModelEntry | undefined): ModelSelection {
  return {
    providerConfig: override?.provider_config ?? '',
    modelName: override?.name ?? '',
    reasoningEffort: override?.reasoning?.effort ?? '',
  }
}

/**
 * Applies a model picker choice to a subagent's override, keeping fields the builder doesn't
 * edit (token limits, cache retention). Clearing the model drops the override when nothing
 * else is left in it.
 */
export function withSubagentModel(
  override: SubagentModelEntry | undefined,
  selection: ModelSelection,
): SubagentModelEntry | undefined {
  const { provider_config: _provider, name: _name, reasoning: _reasoning, ...rest } = override ?? {}
  const next: SubagentModelEntry = { ...rest }
  if (selection.providerConfig !== '' && selection.modelName !== '') {
    next.provider_config = selection.providerConfig
    next.name = selection.modelName
    if (selection.reasoningEffort !== '') next.reasoning = { effort: selection.reasoningEffort }
  }
  return Object.keys(next).length > 0 ? next : undefined
}

export function newSubagent(): BasicSubagent {
  return {
    id: crypto.randomUUID(),
    key: '',
    type: 'profile',
    profileName: '',
    description: '',
    instructionAppend: '',
    maxInstances: '',
    archiveAfterIdleMinutes: '',
  }
}

const subagentKeyPattern = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/

const subagentToolNames = new Set([
  'spawn_agent',
  'read_agent',
  'send_agent_message',
  'stop_agent',
  'list_agents',
])

export function subagentKeyError(key: string): string | undefined {
  if (key === '') return 'Name is required.'
  if (!subagentKeyPattern.test(key)) {
    return 'Name must start with a letter or underscore and use only letters, numbers, and underscores.'
  }
  if (subagentToolNames.has(key)) return 'Name collides with a subagent tool name.'
  return undefined
}

function subagentFieldProblems(subagent: BasicSubagent): string[] {
  const problems: string[] = []
  const keyError = subagentKeyError(subagent.key)
  if (keyError !== undefined) problems.push(keyError)
  if (subagent.type === 'profile') {
    const profileError =
      subagent.profileName === ''
        ? 'Choose an agent profile.'
        : resourceNameError(subagent.profileName, 'Agent profile name')
    if (profileError !== undefined) problems.push(profileError)
  }
  if (!optionalPositiveInt32Valid(subagent.maxInstances)) {
    problems.push('Max instances must be a whole number of 1 or more.')
  }
  if (!optionalPositiveInt32Valid(subagent.archiveAfterIdleMinutes)) {
    problems.push('Archive after idle must be a whole number of minutes, 1 or more.')
  }
  return problems
}

export function subagentValid(subagent: BasicSubagent) {
  return subagentFieldProblems(subagent).length === 0
}

export const maxSubagentDepth = 8

export function maxDepthValid(maxDepth: string) {
  return (
    maxDepth === '' ||
    (optionalPositiveInt32Valid(maxDepth) && Number(maxDepth) <= maxSubagentDepth)
  )
}

export function subagentsProblems(
  subagents: BasicSubagent[],
  maxSubagents: string,
  maxDepth: string,
  preservedSubagents = false,
): string[] {
  const problems: string[] = []
  const keys = subagents.map((subagent) => subagent.key)
  const duplicateKeys = keys.filter((key, index) => key !== '' && keys.indexOf(key) !== index)
  for (const key of new Set(duplicateKeys)) {
    problems.push(`Subagent names must be unique: “${key}”.`)
  }
  subagents.forEach((subagent, index) => {
    const label =
      subagentKeyError(subagent.key) === undefined
        ? `Subagent “${subagent.key}”`
        : `Subagent ${index + 1}`
    for (const problem of subagentFieldProblems(subagent)) problems.push(`${label}: ${problem}`)
  })
  if (!optionalPositiveInt32Valid(maxSubagents)) {
    problems.push('Max active subagents must be a whole number of 1 or more.')
  } else if (maxSubagents !== '' && subagents.length === 0 && !preservedSubagents) {
    problems.push('Max active subagents needs at least one subagent.')
  }
  if (!maxDepthValid(maxDepth)) {
    problems.push(`Max depth must be a whole number from 1 to ${maxSubagentDepth}.`)
  }
  return problems
}

export function subagentWire(subagent: BasicSubagent): SubagentEntry {
  const wire: SubagentEntry = { type: subagent.type }
  if (subagent.type === 'profile') wire.profile = normalizeResourceName(subagent.profileName)
  if (subagent.description.trim() !== '') wire.description = subagent.description.trim()
  if (subagent.modelOverride !== undefined) wire.model = subagent.modelOverride
  const append = normalizeMultiline(subagent.instructionAppend)
  if (append !== '') wire.instruction = { append }
  if (subagent.maxInstances !== '') wire.max_instances = Number(subagent.maxInstances)
  if (subagent.archiveAfterIdleMinutes !== '') {
    wire.archive_after_idle_minutes = Number(subagent.archiveAfterIdleMinutes)
  }
  return wire
}
