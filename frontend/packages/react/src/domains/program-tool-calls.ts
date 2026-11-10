import type { OmnaraClient, ToolCallUpdate } from '@omnara/sdk'
import { listToolCallsInfiniteOptions, listToolCallsInfiniteQueryKey } from '@omnara/sdk/tanstack'
import { type QueryClient, useInfiniteQuery } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import type { AgentChatScope } from './agent-chat-types'
import { openAgentInteractionsQueryKey } from './agent-interactions'
import { cursorPaginated } from './pagination'

function programToolCallsOptions(
  client: OmnaraClient,
  path: AgentChatScope,
  parentToolCallID: string,
) {
  return listToolCallsInfiniteOptions({
    path,
    query: { parent_tool_call_id: parentToolCallID, limit: 100 },
    client,
  })
}

export function useProgramToolCalls(scope: AgentChatScope, parentToolCallID: string) {
  const client = useOmnaraClient()
  return useInfiniteQuery(cursorPaginated(programToolCallsOptions(client, scope, parentToolCallID)))
}

const toolCallRefreshIntervalMs = 500

export class ToolCallQueryRefresher {
  private timer: ReturnType<typeof setTimeout> | null = null
  private pending: (ToolCallUpdate | undefined)[] = []

  constructor(
    private readonly queryClient: QueryClient,
    private readonly client: OmnaraClient,
    private readonly scope: AgentChatScope,
  ) {}

  refresh(update?: ToolCallUpdate): void {
    this.pending.push(update)
    if (this.timer == null) this.flush()
  }

  private flush = (): void => {
    this.timer = null
    if (this.pending.length === 0) return
    const updates = this.pending
    this.pending = []
    this.timer = setTimeout(this.flush, toolCallRefreshIntervalMs)
    void invalidateToolCallQueries(this.queryClient, this.client, this.scope, updates)
  }
}

async function invalidateToolCallQueries(
  queryClient: QueryClient,
  client: OmnaraClient,
  path: AgentChatScope,
  updates: (ToolCallUpdate | undefined)[],
): Promise<void> {
  const invalidations = [
    queryClient.invalidateQueries({ queryKey: openAgentInteractionsQueryKey(client, path) }),
  ]
  if (updates.includes(undefined)) {
    invalidations.push(
      queryClient.invalidateQueries({ queryKey: listToolCallsInfiniteQueryKey({ path, client }) }),
    )
  } else {
    const commands = new Map<string, { agentID: string; parentToolCallID: string }>()
    for (const update of updates) {
      if (!update?.parent_tool_call_id) continue
      commands.set(`${update.agent_id}/${update.parent_tool_call_id}`, {
        agentID: update.agent_id,
        parentToolCallID: update.parent_tool_call_id,
      })
    }
    for (const { agentID, parentToolCallID } of commands.values()) {
      invalidations.push(
        queryClient.invalidateQueries({
          queryKey: programToolCallsOptions(client, { ...path, agentID }, parentToolCallID)
            .queryKey,
        }),
      )
    }
  }
  await Promise.all(invalidations)
}
