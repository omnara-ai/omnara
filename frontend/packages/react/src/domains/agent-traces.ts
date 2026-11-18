import { type OtelTraceVariant, sdk } from '@omnara/sdk'
import { getTurnTraceOptions } from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import type { AgentChatScope } from './agent-chat-types'

const turnsPageSize = 25

/** An agent's turns, newest first, with older pages loaded on demand. */
export function useAgentTurns({ orgID, projectID, agentID }: AgentChatScope) {
  const client = useOmnaraClient()
  return useInfiniteQuery({
    queryKey: ['agent-turns', orgID, projectID, agentID],
    // Turn sequences start at 1, so 0 stands for the newest page.
    initialPageParam: 0,
    queryFn: async ({ pageParam, signal }) => {
      const { data: page } = await sdk.listTurns({
        client,
        path: { orgID, projectID, agentID },
        query: {
          before_turn_sequence: pageParam > 0 ? pageParam : undefined,
          limit: turnsPageSize,
        },
        signal,
      })
      return page
    },
    getNextPageParam: (lastPage) => lastPage.next_before_turn_sequence ?? undefined,
    select: (data) => ({ ...data, turns: data.pages.flatMap((page) => page.data) }),
  })
}

/** One turn rendered as an OpenTelemetry trace. A running turn returns the spans finished so far. */
export function useTurnTrace(
  scope: AgentChatScope,
  turnID: string | undefined,
  variant?: OtelTraceVariant,
) {
  const client = useOmnaraClient()
  return useQuery({
    ...getTurnTraceOptions({
      client,
      path: { ...scope, turnID: turnID ?? '' },
      query: variant ? { variant } : undefined,
    }),
    enabled: turnID !== undefined,
  })
}
