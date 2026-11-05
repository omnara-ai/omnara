import {
  type AgentPanel,
  type CreateAssistantAgentRequest,
  sdk,
  type UpdateAgentPanelRequest,
} from '@omnara/sdk'
import {
  getAgentPanelOptions,
  getAgentPanelQueryKey,
  getOrgOverviewQueryKey,
  listAgentsQueryKey,
  listOrgAgentsQueryKey,
  listPersonalAccessTokensQueryKey,
} from '@omnara/sdk/tanstack'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { invalidateSecretQueries } from './secrets'

export function useAgentPanel() {
  const client = useOmnaraClient()
  return useQuery({
    ...getAgentPanelOptions({ client }),
    // Give initial model provisioning a short fast window; idle tabs and
    // self-hosted projects without a model must not poll indefinitely at 5s.
    refetchInterval: (query) =>
      query.state.data?.setup != null && query.state.dataUpdateCount < 6 ? 5_000 : 60_000,
  })
}

function useAgentPanelCache() {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  const queryKey = getAgentPanelQueryKey({ client })
  function accept(panel: AgentPanel) {
    queryClient.setQueryData<AgentPanel>(queryKey, (current) =>
      current != null && current.revision > panel.revision ? current : panel,
    )
  }
  return { client, queryClient, queryKey, accept }
}

export function useUpdateAgentPanel() {
  const { client, queryClient, queryKey, accept } = useAgentPanelCache()
  return useMutation({
    onMutate: () => queryClient.cancelQueries({ queryKey }),
    mutationFn: async (body: UpdateAgentPanelRequest) => {
      const { data } = await sdk.updateAgentPanel({ client, body })
      return data
    },
    onSuccess: accept,
    onSettled: () => queryClient.invalidateQueries({ queryKey }),
  })
}

export function useCreateAssistantAgent() {
  const { client, queryClient, queryKey, accept } = useAgentPanelCache()
  return useMutation({
    onMutate: () => queryClient.cancelQueries({ queryKey }),
    mutationFn: async ({ orgID, body }: { orgID: string; body: CreateAssistantAgentRequest }) => {
      const { data } = await sdk.createAssistantAgent({ client, path: { orgID }, body })
      return data
    },
    onSuccess: (data, { orgID, body }) => {
      accept(data.panel)
      void queryClient.invalidateQueries({
        queryKey: listAgentsQueryKey({ client, path: { orgID, projectID: body.project_id } }),
      })
      void queryClient.invalidateQueries({ queryKey: listPersonalAccessTokensQueryKey({ client }) })
      void queryClient.invalidateQueries({
        queryKey: listOrgAgentsQueryKey({ client, path: { orgID } }),
      })
      void queryClient.invalidateQueries({
        queryKey: getOrgOverviewQueryKey({ client, path: { orgID } }),
      })
      void invalidateSecretQueries(queryClient, orgID)
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey }),
  })
}
