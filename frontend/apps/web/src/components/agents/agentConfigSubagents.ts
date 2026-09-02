import { normalizeMultiline } from '@/components/agents/agentConfigBasicExtract'
import { normalizeResourceName, resourceNameValid } from '@/lib/resource-name'

export type SubagentType = 'profile' | 'self'

export interface BasicSubagent {
  id: string
  handle: string
  type: SubagentType
  profileName: string
  description: string
  instructionAppend: string
  maxConcurrent: string
  archiveAfterIdleMinutes: string
  modelOverride?: Record<string, unknown>
}

export function newSubagent(): BasicSubagent {
  return {
    id: crypto.randomUUID(),
    handle: '',
    type: 'profile',
    profileName: '',
    description: '',
    instructionAppend: '',
    maxConcurrent: '',
    archiveAfterIdleMinutes: '',
  }
}

const positiveIntegerPattern = /^[1-9][0-9]*$/

export function positiveCountValid(value: string) {
  return value === '' || positiveIntegerPattern.test(value)
}

export const subagentHandlePattern = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/

const subagentToolNames = new Set([
  'spawn_agent',
  'wait_agents',
  'send_agent_message',
  'stop_agent',
  'list_agents',
])

export function subagentHandleError(handle: string): string | undefined {
  if (handle === '') return 'Handle is required.'
  if (!subagentHandlePattern.test(handle)) {
    return 'Handle must start with a letter or underscore and use only letters, numbers, and underscores.'
  }
  if (subagentToolNames.has(handle)) return 'Handle collides with a subagent tool name.'
  return undefined
}

export function subagentValid(subagent: BasicSubagent) {
  return (
    subagentHandleError(subagent.handle) === undefined &&
    (subagent.type === 'self' || resourceNameValid(subagent.profileName)) &&
    positiveCountValid(subagent.maxConcurrent) &&
    positiveCountValid(subagent.archiveAfterIdleMinutes)
  )
}

export function subagentHandlesUnique(subagents: BasicSubagent[]) {
  const handles = subagents.map((subagent) => subagent.handle)
  return new Set(handles).size === handles.length
}

export function subagentsValid(subagents: BasicSubagent[], maxSubagents: string) {
  return (
    subagentHandlesUnique(subagents) &&
    subagents.every(subagentValid) &&
    positiveCountValid(maxSubagents) &&
    (maxSubagents === '' || subagents.length > 0)
  )
}

export function subagentWire(subagent: BasicSubagent): Record<string, unknown> {
  const wire: Record<string, unknown> = { type: subagent.type }
  if (subagent.type === 'profile') wire.profile = normalizeResourceName(subagent.profileName)
  if (subagent.description.trim() !== '') wire.description = subagent.description.trim()
  if (subagent.modelOverride !== undefined) wire.model = subagent.modelOverride
  const append = normalizeMultiline(subagent.instructionAppend)
  if (append !== '') wire.instruction = { append }
  if (subagent.maxConcurrent !== '') wire.max_concurrent = Number(subagent.maxConcurrent)
  if (subagent.archiveAfterIdleMinutes !== '') {
    wire.archive_after_idle_minutes = Number(subagent.archiveAfterIdleMinutes)
  }
  return wire
}
