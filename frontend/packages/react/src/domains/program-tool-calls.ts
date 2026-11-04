import type { OmnaraClient } from '@omnara/sdk'
import { listToolCallsInfiniteOptions, listToolCallsInfiniteQueryKey } from '@omnara/sdk/tanstack'
import { type QueryClient, useInfiniteQuery } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import type { AgentChatScope } from './agent-chat-types'
import { openAgentInteractionsQueryKey } from './agent-interactions'
import { cursorPaginated } from './pagination'

/**
 * The tool calls a run_command call's process made with `omnarad call`. They
 * write no agent events, so the chat session refreshes these queries from
 * tool_call_update stream frames instead.
 */
export function useProgramToolCalls(
  orgID: string,
  projectID: string,
  agentID: string,
  parentToolCallID: string,
  options?: { enabled?: boolean },
) {
  const client = useOmnaraClient()
  return useInfiniteQuery({
    ...cursorPaginated(
      listToolCallsInfiniteOptions({
        path: { orgID, projectID, agentID },
        query: { parent_tool_call_id: parentToolCallID, limit: 100 },
        client,
      }),
    ),
    enabled: options?.enabled,
  })
}

/**
 * Refreshes what tool_call_update frames report: open interactions, and the
 * tool calls commands make, which write no agent events.
 */
export async function invalidateToolCallQueries(
  queryClient: QueryClient,
  client: OmnaraClient,
  scope: AgentChatScope,
): Promise<void> {
  const path = { orgID: scope.orgID, projectID: scope.projectID, agentID: scope.agentID }
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: openAgentInteractionsQueryKey(client, path) }),
    queryClient.invalidateQueries({ queryKey: listToolCallsInfiniteQueryKey({ path, client }) }),
  ])
}
