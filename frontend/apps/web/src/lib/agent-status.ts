import type { Agent, AgentActivity } from '@omnara/sdk'

export const statusLabels: Record<AgentActivity['state'], string> = {
  running: 'Working',
  waiting_on_interaction: 'Waiting',
  idle: 'Idle',
  archived: 'Archived',
}

export const activityFilterOptions = [
  { value: 'all', label: 'Any status' },
  { value: 'running', label: statusLabels.running },
  { value: 'waiting_on_interaction', label: statusLabels.waiting_on_interaction },
  { value: 'idle', label: statusLabels.idle },
  { value: 'archived', label: statusLabels.archived },
] as const

export type ActivityFilterValue = (typeof activityFilterOptions)[number]['value']

export function agentStatusLabel(agent: Agent) {
  const activity = agent.activity
  if (activity?.state === 'idle' && hasBackgroundWork(activity)) return 'Background work'
  const state = activity?.state ?? (agent.state === 'archived' ? 'archived' : undefined)
  return state && statusLabels[state]
}

export function hasBackgroundWork(activity: AgentActivity) {
  return activity.running_processes.count > 0 || activity.working_subagents.count > 0
}
