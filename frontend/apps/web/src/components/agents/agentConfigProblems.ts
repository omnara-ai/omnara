import { mcpServersProblems } from '@/components/agents/agentConfigMcp'
import { subagentsProblems } from '@/components/agents/agentConfigSubagents'
import type { BasicConfig, BasicMachineSource } from '@/components/agents/useAgentBuilderForm'
import { secretRowsValid, textRowsValid } from '@/components/key-value/keyValueRows'
import {
  optionalIdleDeletionMinutesValid,
  optionalPositiveInt32Valid,
} from '@/components/machines/machineOverrides'
import { memoryGbDraftValid } from '@/lib/machine-memory'
import { resourceNameError, resourceNameValid } from '@/lib/resource-name'

interface BuilderAvailability {
  modelUnavailable: boolean
  unavailableSourceIds: string[]
  unavailableSkillIds: string[]
}

const allAvailable: BuilderAvailability = {
  modelUnavailable: false,
  unavailableSourceIds: [],
  unavailableSkillIds: [],
}

export function configProblems(yaml: string, builderProblems: string[]): string[] {
  if (builderProblems.length > 0) return builderProblems
  return yaml.trim() === '' ? ['The configuration is empty.'] : []
}

export function basicConfigProblems(
  draft: BasicConfig,
  availability: BuilderAvailability = allAvailable,
): string[] {
  const problems: string[] = []
  if (draft.providerConfig === '' || draft.modelName === '') {
    problems.push('Choose a model.')
  } else {
    const modelError =
      resourceNameError(draft.providerConfig, 'Model provider config name') ??
      resourceNameError(draft.modelName, 'Model name')
    if (modelError !== undefined) {
      problems.push(modelError)
    } else if (availability.modelUnavailable) {
      problems.push('The selected model or reasoning effort can’t be used.')
    }
  }
  if (draft.instruction.trim() === '') problems.push('Instructions are required.')
  const unavailableSourceIds = new Set(availability.unavailableSourceIds)
  draft.machineSources.forEach((source, index) => {
    const label = resourceNameValid(source.name)
      ? `Machine source “${source.name}”`
      : `Machine source ${index + 1}`
    for (const problem of machineSourceProblems(source)) problems.push(`${label}: ${problem}`)
    if (unavailableSourceIds.has(source.id)) {
      problems.push(`${label}: No longer available to this project. Pick another or remove it.`)
    }
  })
  if (availability.unavailableSkillIds.length > 0) {
    problems.push('Remove skills that are no longer available to this project.')
  }
  problems.push(...subagentsProblems(draft.subagents, draft.maxSubagents, draft.maxDepth))
  problems.push(...mcpServersProblems(draft.mcpServers))
  return [...new Set(problems)]
}

const nonNegativeIntegerPattern = /^(0|[1-9][0-9]*)$/

function machineCountValid(value: string) {
  return value === '' || nonNegativeIntegerPattern.test(value)
}

function machineSourceProblems(source: BasicMachineSource): string[] {
  const problems: string[] = []
  const pool = source.kind === 'pool'
  const nameError =
    source.name === ''
      ? `Choose a ${pool ? 'machine pool' : 'machine'}.`
      : resourceNameError(source.name, pool ? 'Machine pool name' : 'Machine name')
  if (nameError !== undefined) problems.push(nameError)
  if (!textRowsValid(source.envRows)) problems.push('Each variable needs a unique name.')
  if (!secretRowsValid(source.secretEnvRows)) {
    problems.push('Each secret variable needs a unique name and a secret.')
  }
  if (!pool) return problems
  if (!machineCountValid(source.initialNumMachines)) {
    problems.push('Initial machines must be a whole number of 0 or more.')
  }
  if (!machineCountValid(source.maxMachines)) {
    problems.push('Max machines must be a whole number of 0 or more.')
  }
  if (!optionalIdleDeletionMinutesValid(source.deleteAfterIdleMinutes)) {
    problems.push('Delete after idle minutes must be 0 or a whole number of at least 5.')
  }
  if (!optionalPositiveInt32Valid(source.machineCpu)) {
    problems.push('Machine CPU must be a whole number of 1 or more.')
  }
  if (!memoryGbDraftValid(source.machineMemoryGb, { optional: true })) {
    problems.push('Machine memory must be greater than 0 GB.')
  }
  return problems
}
