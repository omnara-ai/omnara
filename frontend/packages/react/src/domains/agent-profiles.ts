import { type ListAgentProfilesData, sdk, type UpdateAgentProfileRequest } from '@omnara/sdk'
import {
  getAgentProfileOptions,
  getAgentProfileQueryKey,
  getOrgOverviewQueryKey,
  listAgentProfilesInfiniteOptions,
  listAgentProfilesQueryKey,
} from '@omnara/sdk/tanstack'
import {
  type QueryClient,
  type QueryKey,
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
import { cursorPagination } from './pagination'
import { useScopedMutation } from './scoped-mutation'

export type AgentProfileListFilters = ListFilters<ListAgentProfilesData>
export type AgentProfileListSort = ListSort<ListAgentProfilesData>
export type AgentProfileListOptions = PaginatedListOptions<ListAgentProfilesData>

export function useAgentProfiles(
  orgID: string,
  projectID: string,
  options?: AgentProfileListOptions,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListAgentProfilesData>(options)
  return useInfiniteQuery({
    ...listAgentProfilesInfiniteOptions({
      path: { orgID, projectID },
      query: list.query,
      client,
    }),
    ...cursorPagination,
    enabled: list.enabled,
  })
}

export function useAgentProfile(orgID: string, projectID: string, agentProfileID: string) {
  const client = useOmnaraClient()
  return useSuspenseQuery(
    getAgentProfileOptions({ path: { orgID, projectID, agentProfileID }, client }),
  )
}

export function useAgentProfileQuery(orgID: string, projectID: string, agentProfileID?: string) {
  const client = useOmnaraClient()
  return useQuery({
    ...getAgentProfileOptions({
      path: { orgID, projectID, agentProfileID: agentProfileID ?? '' },
      client,
    }),
    enabled: agentProfileID !== undefined,
  })
}

export function useCreateAgentProfile(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createAgentProfile,
    { orgID, projectID },
    {
      onSuccess: async () => {
        await Promise.all([
          queryClient.invalidateQueries({
            queryKey: listAgentProfilesQueryKey({ path: { orgID, projectID }, client }),
          }),
          queryClient.invalidateQueries({
            queryKey: getOrgOverviewQueryKey({ path: { orgID }, client }),
          }),
        ])
      },
    },
  )
}

export function useCreateSlackSetup(orgID: string, projectID: string, agentProfileID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createSlackSetup,
    { orgID, projectID, agentProfileID },
    {
      onError: async () => {
        await Promise.all([
          queryClient.invalidateQueries({
            queryKey: getAgentProfileQueryKey({
              path: { orgID, projectID, agentProfileID },
              client,
            }),
          }),
          queryClient.invalidateQueries({
            queryKey: listAgentProfilesQueryKey({ path: { orgID, projectID }, client }),
          }),
        ])
      },
    },
  )
}

export function useUpdateAgentProfile(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      agentProfileID,
      ...body
    }: UpdateAgentProfileRequest & { agentProfileID: string }) => {
      const { data } = await sdk.updateAgentProfile({
        path: { orgID, projectID, agentProfileID },
        body,
        client,
      })
      return data
    },
    onSuccess: async (data, { agentProfileID }) => {
      queryClient.setQueryData(
        getAgentProfileQueryKey({ path: { orgID, projectID, agentProfileID }, client }),
        data,
      )
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: listAgentProfilesQueryKey({ path: { orgID, projectID }, client }),
        }),
        queryClient.invalidateQueries({
          queryKey: getAgentProfileQueryKey({
            path: { orgID, projectID, agentProfileID },
            client,
          }),
        }),
      ])
    },
  })
}

export function useRenameAgentProfile(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ agentProfileID, name }: { agentProfileID: string; name: string }) => {
      const { data } = await sdk.renameAgentProfile({
        path: { orgID, projectID, agentProfileID },
        body: { name },
        client,
      })
      return data
    },
    onSuccess: async (data, { agentProfileID }) => {
      queryClient.setQueryData(
        getAgentProfileQueryKey({ path: { orgID, projectID, agentProfileID }, client }),
        data,
      )
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: listAgentProfilesQueryKey({ path: { orgID, projectID }, client }),
        }),
        queryClient.invalidateQueries({
          queryKey: getAgentProfileQueryKey({
            path: { orgID, projectID, agentProfileID },
            client,
          }),
        }),
        queryClient.invalidateQueries({
          queryKey: getOrgOverviewQueryKey({ path: { orgID }, client }),
        }),
      ])
    },
  })
}

export function useDeleteAgentProfile(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (agentProfileID: string) => {
      const { data } = await sdk.deleteAgentProfile({
        path: { orgID, projectID, agentProfileID },
        client,
      })
      return data
    },
    onSuccess: async (_data, agentProfileID) => {
      removeQueryWhenInactive(
        queryClient,
        getAgentProfileQueryKey({ path: { orgID, projectID, agentProfileID }, client }),
      )
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: listAgentProfilesQueryKey({ path: { orgID, projectID }, client }),
        }),
        queryClient.invalidateQueries({
          queryKey: getOrgOverviewQueryKey({ path: { orgID }, client }),
        }),
      ])
    },
  })
}

function removeQueryWhenInactive(queryClient: QueryClient, queryKey: QueryKey) {
  const cache = queryClient.getQueryCache()
  const query = cache.find({ queryKey, exact: true })
  if (!query) return
  if (query.getObserversCount() === 0) {
    cache.remove(query)
    return
  }
  const unsubscribe = cache.subscribe((event) => {
    if (event.query !== query) return
    if (event.type === 'removed') {
      unsubscribe()
      return
    }
    if (event.type === 'observerRemoved' && query.getObserversCount() === 0) {
      unsubscribe()
      cache.remove(query)
    }
  })
}
