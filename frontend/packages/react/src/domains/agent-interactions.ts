import { type ListAgentInteractionsResponse, type OmnaraClient, sdk } from '@omnara/sdk'
import {
  getAgentQueryKey,
  listAgentInteractionsOptions,
  listAgentInteractionsQueryKey,
  listAgentsQueryKey,
} from '@omnara/sdk/tanstack'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
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
 * this query from stream frames: the agent's own tool-call events and updates,
 * and subagent_interaction frames for its subagents.
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
