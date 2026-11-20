import { type OtelTraceVariant, sdk } from '@omnara/sdk'
import { getTurnTraceOptions } from '@omnara/sdk/tanstack'
import { type Query, type QueryClient, useInfiniteQuery, useQuery } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import type { AgentChatScope } from './agent-chat-types'
import { generatedQueryKey } from './query-keys'

const turnsPageSize = 25

export function agentTurnsQueryKey({ orgID, projectID, agentID }: AgentChatScope) {
  return ['agent-turns', orgID, projectID, agentID] as const
}

export function turnTraceQueryPredicate(scope: AgentChatScope, turnID?: string) {
  return (query: Query) => {
    const entry = generatedQueryKey(query)
    if (entry?._id !== 'getTurnTrace') return false
    const path = entry.path
    if (!path) return false
    if (
      path.orgID !== scope.orgID ||
      path.projectID !== scope.projectID ||
      path.agentID !== scope.agentID
    ) {
      return false
    }
    return turnID === undefined || path.turnID === turnID
  }
}

/**
 * Coalesces trace and turn-list query invalidations while an agent streams
 * durable events, so rapid tool/model events trigger one refresh per window.
 */
export class TraceInvalidationCoalescer {
  private readonly queryClient: QueryClient
  private readonly scope: AgentChatScope
  private readonly delayMs: number
  private seenTurnIDs = new Set<string>()
  private pendingTurns = false
  private pendingAllTraces = false
  private pendingTurnIDs = new Set<string>()
  private timer: ReturnType<typeof setTimeout> | null = null

  constructor(queryClient: QueryClient, scope: AgentChatScope, delayMs: number) {
    this.queryClient = queryClient
    this.scope = scope
    this.delayMs = delayMs
  }

  onEvent(turnID: string | undefined, isOpening: boolean): void {
    if (turnID) {
      if (isOpening || !this.seenTurnIDs.has(turnID)) {
        this.seenTurnIDs.add(turnID)
        this.pendingTurns = true
      }
      this.pendingTurnIDs.add(turnID)
    } else {
      this.pendingTurns = true
    }
    this.schedule()
  }

  onReconnect(): void {
    this.pendingTurns = true
    this.pendingAllTraces = true
    this.schedule()
  }

  cancel(): void {
    if (this.timer !== null) {
      clearTimeout(this.timer)
      this.timer = null
    }
    this.pendingTurns = false
    this.pendingAllTraces = false
    this.pendingTurnIDs.clear()
  }

  private schedule(): void {
    if (this.timer !== null) return
    this.timer = setTimeout(() => {
      this.timer = null
      this.flush()
    }, this.delayMs)
  }

  private flush(): void {
    const invalidateTurns = this.pendingTurns
    const invalidateAll = this.pendingAllTraces
    const turnIDs = [...this.pendingTurnIDs]
    this.pendingTurns = false
    this.pendingAllTraces = false
    this.pendingTurnIDs.clear()
    if (invalidateTurns) {
      void this.queryClient.invalidateQueries({ queryKey: agentTurnsQueryKey(this.scope) })
    }
    if (invalidateAll) {
      void this.queryClient.invalidateQueries({ predicate: turnTraceQueryPredicate(this.scope) })
      return
    }
    for (const turnID of turnIDs) {
      void this.queryClient.invalidateQueries({
        predicate: turnTraceQueryPredicate(this.scope, turnID),
      })
    }
  }
}

/** An agent's turns, newest first, with older pages loaded on demand. */
export function useAgentTurns(scope: AgentChatScope) {
  const client = useOmnaraClient()
  const { orgID, projectID, agentID } = scope
  return useInfiniteQuery({
    queryKey: agentTurnsQueryKey(scope),
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
