import { isScheduledBacklogInput, useAgentInputBacklog } from '@omnara/react'

export interface AgentScope {
  orgId: string
  projectId: string
  agentId: string
}

export function useScheduledMessages({ orgId, projectId, agentId }: AgentScope) {
  const backlog = useAgentInputBacklog({ orgID: orgId, projectID: projectId, agentID: agentId })
  const scheduled = (backlog.query.data?.data ?? []).filter(isScheduledBacklogInput)
  return { scheduled, cancel: backlog.cancel }
}
