import { type OmnaraClient, sdk } from '@omnara/sdk'
import { useInfiniteQuery } from '@tanstack/react-query'

import { sequenceNumber } from './agent-chat-messages'
import type { AgentChatScope } from './agent-chat-types'

const historyPageSize = 100

export function agentChatHistoryQueryKey(scope: AgentChatScope) {
  return ['agent-chat-history', scope.orgID, scope.projectID, scope.agentID]
}

export function useAgentChatHistory(client: OmnaraClient, scope: AgentChatScope) {
  const { orgID, projectID, agentID } = scope
  return useInfiniteQuery({
    queryKey: agentChatHistoryQueryKey({ orgID, projectID, agentID }),
    initialPageParam: 0,
    queryFn: async ({ pageParam, signal }) => {
      const { data: page } = await sdk.listEvents({
        client,
        path: { orgID, projectID, agentID },
        query: { before_sequence: pageParam, limit: historyPageSize },
        signal,
      })
      return page
    },
    getNextPageParam: (lastPage) => {
      const nextBeforeSequence = lastPage.next_before_sequence
      return nextBeforeSequence == null ? undefined : sequenceNumber(nextBeforeSequence)
    },
    select: (data) => ({
      ...data,
      events: [...data.pages].reverse().flatMap((page) => page.data),
    }),
    staleTime: Infinity,
  })
}
