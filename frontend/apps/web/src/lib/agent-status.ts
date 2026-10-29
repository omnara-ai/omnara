import type { Agent, AgentActivity } from '@omnara/sdk'

const statusLabels: Record<AgentActivity['state'], string> = {
  running: 'Working',
  waiting_on_interaction: 'Waiting',
  idle: 'Idle',
  archived: 'Archived',
}

export function agentStatusLabel(agent: Agent) {
  const activity = agent.activity
  if (activity?.state === 'idle' && hasBackgroundWork(activity)) return 'Background work'
  const state = activity?.state ?? (agent.state === 'archived' ? 'archived' : undefined)
  return state && statusLabels[state]
}

/** Whether the agent has processes running or subagents working, whatever its own state. */
export function hasBackgroundWork(activity: AgentActivity) {
  return activity.running_processes.count > 0 || activity.working_subagents.count > 0
}
