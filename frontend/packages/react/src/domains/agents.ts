import { type ListAgentsData, sdk } from '@omnara/sdk'
import {
  getAgentOptions,
  getOrgOverviewQueryKey,
  listAgentsInfiniteOptions,
  listAgentsQueryKey,
} from '@omnara/sdk/tanstack'
import {
  useInfiniteQuery,
  useMutation,
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
import { cursorPagination } from './pagination'
import { useScopedMutation } from './scoped-mutation'

export type AgentListFilters = ListFilters<ListAgentsData>
export type AgentListSort = ListSort<ListAgentsData>
export type AgentListOptions = PaginatedListOptions<ListAgentsData>

export function useAgents(orgID: string, projectID: string, options?: AgentListOptions) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListAgentsData>(options)
  return useInfiniteQuery({
    ...listAgentsInfiniteOptions({
      path: { orgID, projectID },
      query: list.query,
      client,
    }),
    ...cursorPagination,
    enabled: list.enabled,
  })
}

export function useAgent(orgID: string, projectID: string, agentID: string) {
  const client = useOmnaraClient()
  return useSuspenseQuery(getAgentOptions({ path: { orgID, projectID, agentID }, client }))
}

export function useCreateAgentConfig(orgID: string, projectID: string) {
  return useScopedMutation(sdk.createAgentConfig, { orgID, projectID })
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
  })
}
