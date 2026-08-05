import { type OmnaraClient, sdk } from '@omnara/sdk'
import { listAgentInteractionsOptions, listAgentInteractionsQueryKey } from '@omnara/sdk/tanstack'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'

const openInteractionsQuery = { state: 'open', limit: 100 } as const
const activeAgentRefetchIntervalMs = 1000

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
 * Open interactions for an agent. Interaction rows may be created after the
 * corresponding tool-call event, so active agents are polled in addition to
 * the immediate refreshes triggered by chat events.
 */
export function useAgentInteractions(
  orgID: string,
  projectID: string,
  agentID: string,
  agentActive: boolean,
) {
  const client = useOmnaraClient()
  return useQuery({
    ...listAgentInteractionsOptions({
      path: { orgID, projectID, agentID },
      query: openInteractionsQuery,
      client,
    }),
    refetchInterval: agentActive ? activeAgentRefetchIntervalMs : false,
  })
}

export function useResolveAgentInteraction(orgID: string, projectID: string, agentID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      interactionID,
      body,
    }: {
      interactionID: string
      body: Parameters<typeof sdk.resolveAgentInteraction>[0]['body']
    }) => {
      const { data } = await sdk.resolveAgentInteraction({
        client,
        path: { orgID, projectID, agentID, interactionID },
        body,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: openAgentInteractionsQueryKey(client, { orgID, projectID, agentID }),
      })
    },
  })
}

export function useCancelAgent(orgID: string, projectID: string, agentID: string) {
  const client = useOmnaraClient()
  return useMutation({
    mutationFn: async () => {
      const { data } = await sdk.cancelAgent({
        client,
        path: { orgID, projectID, agentID },
      })
      return data
    },
  })
}
