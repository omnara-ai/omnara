import { ApiError, type GetAgentResponse, type ListAgentsData, sdk } from '@omnara/sdk'
import {
  getAgentConfigOptions,
  getAgentOptions,
  getAgentQueryKey,
  getOrgOverviewQueryKey,
  listAgentsInfiniteOptions,
  listAgentsQueryKey,
} from '@omnara/sdk/tanstack'
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  useSuspenseQuery,
} from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import {
  type ListFilters,
  type ListSort,
  type PaginatedListOptions,
  paginatedListOptions,
} from './list-options'
import { cursorPaginated } from './pagination'
import { useScopedMutation } from './scoped-mutation'

export type AgentListFilters = ListFilters<ListAgentsData>
export type AgentListSort = ListSort<ListAgentsData>
export type AgentListOptions = PaginatedListOptions<ListAgentsData>

export function useAgents(orgID: string, projectID: string, options?: AgentListOptions) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListAgentsData>(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listAgentsInfiniteOptions({
        path: { orgID, projectID },
        query: list.query,
        client,
      }),
    ),
    enabled: list.enabled,
  })
}

export function useAgent(
  orgID: string,
  projectID: string,
  agentID: string,
  options?: { refetchInterval?: (data: GetAgentResponse | undefined) => number | false },
) {
  const client = useOmnaraClient()
  const refetchInterval = options?.refetchInterval
  return useSuspenseQuery({
    ...getAgentOptions({ path: { orgID, projectID, agentID }, client }),
    refetchInterval:
      refetchInterval == null ? undefined : (query) => refetchInterval(query.state.data),
  })
}

export function useCreateAgentConfig(orgID: string, projectID: string) {
  return useScopedMutation(sdk.createAgentConfig, { orgID, projectID })
}

export function useAgentConfig(orgID: string, projectID: string, agentConfigID?: string) {
  const client = useOmnaraClient()
  return useQuery({
    ...getAgentConfigOptions({
      path: { orgID, projectID, agentConfigID: agentConfigID ?? '' },
      client,
    }),
    enabled: agentConfigID !== undefined,
  })
}

export function useUpdateAgentConfig(orgID: string, projectID: string, agentID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  const invalidateAgent = async () => {
    await queryClient.invalidateQueries({
      queryKey: getAgentQueryKey({ path: { orgID, projectID, agentID }, client }),
    })
  }
  return useScopedMutation(
    sdk.updateAgentConfig,
    { orgID, projectID, agentID },
    {
      onSuccess: invalidateAgent,
      onError: async (error) => {
        if (error instanceof ApiError && error.status === 409) {
          await invalidateAgent()
        }
      },
    },
  )
}

export function useCreateAgent(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createAgent,
    { orgID, projectID },
    {
      onSuccess: async () => {
        await Promise.all([
          queryClient.invalidateQueries({
            queryKey: listAgentsQueryKey({ path: { orgID, projectID }, client }),
          }),
          queryClient.invalidateQueries({
            queryKey: getOrgOverviewQueryKey({ path: { orgID }, client }),
          }),
        ])
      },
    },
  )
}

export function useArchiveAgent(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (agentID: string) => {
      const { data } = await sdk.archiveAgent({ path: { orgID, projectID, agentID }, client })
      return data
    },
    onSuccess: async (data, agentID) => {
      const agentQueryKey = getAgentQueryKey({ path: { orgID, projectID, agentID }, client })
      queryClient.setQueryData<GetAgentResponse>(agentQueryKey, (current) =>
        current === undefined ? current : { ...current, agent: data.agent },
      )
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: agentQueryKey }),
        queryClient.invalidateQueries({
          queryKey: listAgentsQueryKey({ path: { orgID, projectID }, client }),
        }),
        queryClient.invalidateQueries({
          queryKey: getOrgOverviewQueryKey({ path: { orgID }, client }),
        }),
      ])
    },
  })
}
