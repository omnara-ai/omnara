import {
  type Actor,
  type AgentEvent,
  type ListAgentInteractionsResponse,
  type OmnaraClient,
  sdk,
} from '@omnara/sdk'
import {
  getActorOptions,
  getAgentQueryKey,
  listAgentInteractionsOptions,
  listAgentInteractionsQueryKey,
  listAgentsQueryKey,
} from '@omnara/sdk/tanstack'
import { type QueryClient, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import type { AgentChatScope } from './agent-chat-types'
import { agentInputBacklogQueryKey } from './agent-input-backlog'

const openInteractionsQuery = { state: 'open', limit: 100, include_subagents: true } as const

/** The query key for an agent's open interactions, shared by everything that
 * reads or invalidates them (the hook, the resolve mutation, and the chat
 * session). */
export function openAgentInteractionsQueryKey(
  client: OmnaraClient,
  path: { orgID: string; projectID: string; agentID: string },
) {
  return listAgentInteractionsQueryKey({ path, query: openInteractionsQuery, client })
}

/**
 * Open interactions for an agent and its subagents. The chat session refreshes
 * this query from stream frames: tool-call events and tool_call_update frames,
 * which the stream sends for the agent and every subagent beneath it.
 */
export function useAgentInteractions(orgID: string, projectID: string, agentID: string) {
  const client = useOmnaraClient()
  return useQuery(
    listAgentInteractionsOptions({
      path: { orgID, projectID, agentID },
      query: openInteractionsQuery,
      client,
    }),
  )
}

export function useResolveAgentInteraction(orgID: string, projectID: string, agentID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  const queryKey = openAgentInteractionsQueryKey(client, { orgID, projectID, agentID })
  return useMutation({
    mutationFn: async ({
      interactionID,
      body,
      targetAgentID = agentID,
    }: {
      interactionID: string
      body: Parameters<typeof sdk.resolveAgentInteraction>[0]['body']
      targetAgentID?: string
    }) => {
      const { data } = await sdk.resolveAgentInteraction({
        client,
        path: { orgID, projectID, agentID: targetAgentID, interactionID },
        body,
      })
      return data
    },
    onMutate: async ({ interactionID }) => {
      await queryClient.cancelQueries({ queryKey })
      const previous = queryClient.getQueryData<ListAgentInteractionsResponse>(queryKey)
      queryClient.setQueryData<ListAgentInteractionsResponse>(queryKey, (current) =>
        current == null
          ? current
          : { ...current, data: current.data.filter((item) => item.id !== interactionID) },
      )
      return { previous }
    },
    onError: (_error, _variables, context) => {
      if (context?.previous != null) queryClient.setQueryData(queryKey, context.previous)
    },
    onSettled: async () => {
      await queryClient.invalidateQueries({ queryKey })
    },
  })
}

export function useCancelAgent(orgID: string, projectID: string, agentID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async () => {
      const { data } = await sdk.cancelAgent({
        client,
        path: { orgID, projectID, agentID },
      })
      return data
    },
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: getAgentQueryKey({ path: { orgID, projectID, agentID }, client }),
        }),
        queryClient.invalidateQueries({
          queryKey: listAgentsQueryKey({ path: { orgID, projectID }, client }),
        }),
        queryClient.invalidateQueries({
          queryKey: openAgentInteractionsQueryKey(client, { orgID, projectID, agentID }),
        }),
        queryClient.invalidateQueries({
          queryKey: agentInputBacklogQueryKey(client, { orgID, projectID, agentID }),
        }),
      ])
    },
  })
}

function spawnsSubagent(
  event: AgentEvent,
  hasEvent: (matches: (event: AgentEvent) => boolean) => boolean,
): boolean {
  if (event.event_kind !== 'tool_result') return false
  return hasEvent(
    (candidate) =>
      candidate.event_kind === 'model_output' &&
      candidate.content_blocks.some(
        (block) =>
          block.type === 'tool_call' &&
          block.tool_call_id === event.tool_call_id &&
          block.name === 'spawn_agent',
      ),
  )
}

function isAgentActor(actor: Actor): boolean {
  return actor.provider === 'omnara' && actor.provider_user_id.startsWith('agt_')
}

async function sentBySubagent(
  queryClient: QueryClient,
  client: OmnaraClient,
  scope: AgentChatScope,
  event: AgentEvent,
): Promise<boolean> {
  if (event.event_kind !== 'agent_input' || event.actor_id == null) return false
  const actor = await queryClient
    .ensureQueryData(
      getActorOptions({
        path: { orgID: scope.orgID, projectID: scope.projectID, actorID: event.actor_id },
        client,
      }),
    )
    .catch(() => null)
  return actor != null && isAgentActor(actor)
}

export async function invalidateSubagentList(
  queryClient: QueryClient,
  client: OmnaraClient,
  scope: AgentChatScope,
  event: AgentEvent,
  hasEvent: (matches: (event: AgentEvent) => boolean) => boolean,
): Promise<void> {
  if (
    !spawnsSubagent(event, hasEvent) &&
    !(await sentBySubagent(queryClient, client, scope, event))
  ) {
    return
  }
  await queryClient.invalidateQueries({
    queryKey: listAgentsQueryKey({
      path: { orgID: scope.orgID, projectID: scope.projectID },
      client,
    }),
  })
}
