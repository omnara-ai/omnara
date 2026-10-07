import type { Agent } from '@omnara/sdk'

/** How often a query holding an active agent refetches, so its status settles once the agent finishes. */
export const activeAgentPollIntervalMs = 5_000

/** Whether the agent is working or waiting on someone, so its shown status can still change. */
export function isAgentActive(agent: Agent) {
  const state = agent.activity?.state
  return state === 'running' || state === 'waiting_on_interaction'
}

/**
 * Polls while any of the agents is active. A caller's own interval wins when it returns
 * one, so a query can also poll for reasons of its own.
 */
export function activeAgentsRefetchInterval<T>(
  getAgents: (data: T | undefined) => Agent[],
  refetchInterval?: (data: T | undefined) => number | false,
) {
  return (query: { state: { data?: T } }) => {
    const data = query.state.data
    const own = refetchInterval?.(data)
    if (own) return own
    return getAgents(data).some(isAgentActive) ? activeAgentPollIntervalMs : false
  }
}
