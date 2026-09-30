import type { Agent, AgentActivity } from '@omnara/sdk'

const statusLabels: Record<AgentActivity['state'], string> = {
  running: 'Working',
  waiting_on_interaction: 'Waiting',
  idle: 'Idle',
  archived: 'Archived',
}

export function agentStatusLabel(agent: Agent) {
  const state = agent.activity?.state ?? (agent.state === 'archived' ? 'archived' : undefined)
  return state && statusLabels[state]
}

export function isAgentActive(agent: Agent) {
  const state = agent.activity?.state
  return state === 'running' || state === 'waiting_on_interaction'
}
