import type { AgentChange, OmnaraClient } from '@omnara/sdk'
import { getAgentQueryKey, listAgentsQueryKey } from '@omnara/sdk/tanstack'
import type { QueryClient, QueryKey } from '@tanstack/react-query'

import type { AgentChatScope } from './agent-chat-types'
import { openAgentInteractionsQueryKey } from './agent-interactions'

export function refreshAgentQuery(queryClient: QueryClient, queryKey: QueryKey): void {
  // Invalidation can reuse an initial fetch with no cached data. Cancel first
  // so recovery and change notifications always trigger a read started afterward.
  const filters = { queryKey }
  void queryClient.cancelQueries(filters).then(() => queryClient.invalidateQueries(filters))
}

export function invalidateAgentChildList(
  queryClient: QueryClient,
  client: OmnaraClient,
  { orgID, projectID, agentID }: AgentChatScope,
): void {
  refreshAgentQuery(
    queryClient,
    listAgentsQueryKey({
      client,
      path: { orgID, projectID },
      query: { parent_agent_id: agentID },
    }),
  )
}

export function invalidateAgentChange(
  queryClient: QueryClient,
  client: OmnaraClient,
  scope: AgentChatScope,
  change: AgentChange,
): void {
  const interactionsChanged = change.changes.includes('interactions')
  if (interactionsChanged) {
    refreshAgentQuery(queryClient, openAgentInteractionsQueryKey(client, scope))
  }
  if (!interactionsChanged && !change.changes.includes('agent')) return
  if (change.parent_agent_id != null) {
    invalidateAgentChildList(queryClient, client, { ...scope, agentID: change.parent_agent_id })
  }
  refreshAgentQuery(
    queryClient,
    getAgentQueryKey({ client, path: { ...scope, agentID: change.agent_id } }),
  )
}
