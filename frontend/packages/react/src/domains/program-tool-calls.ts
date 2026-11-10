import type { OmnaraClient, ToolCallUpdate } from '@omnara/sdk'
import { listToolCallsInfiniteOptions, listToolCallsInfiniteQueryKey } from '@omnara/sdk/tanstack'
import { type QueryClient, useInfiniteQuery } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import type { AgentChatScope } from './agent-chat-types'
import { openAgentInteractionsQueryKey } from './agent-interactions'
import { cursorPaginated } from './pagination'

function programToolCallsOptions(
  client: OmnaraClient,
  path: { orgID: string; projectID: string; agentID: string },
  parentToolCallID: string,
) {
  return listToolCallsInfiniteOptions({
    path,
    query: { parent_tool_call_id: parentToolCallID, limit: 100 },
    client,
  })
}

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
) {
  const client = useOmnaraClient()
  return useInfiniteQuery(
    cursorPaginated(
      programToolCallsOptions(client, { orgID, projectID, agentID }, parentToolCallID),
    ),
  )
}

const toolCallRefreshIntervalMs = 500

/**
 * Refreshes what tool_call_update frames report: open interactions and, for a
 * program call, its command's tool calls, which write no agent events. Without
 * a frame, as after a reconnect, every command's tool calls refresh.
 *
 * A command can make hundreds of calls, so the first frame refreshes at once
 * and the frames of the next interval share one refresh at its end.
 */
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
  scope: AgentChatScope,
  updates: (ToolCallUpdate | undefined)[],
): Promise<void> {
  const path = { orgID: scope.orgID, projectID: scope.projectID, agentID: scope.agentID }
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
