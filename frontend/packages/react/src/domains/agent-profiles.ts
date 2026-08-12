import { type ListAgentProfilesData, sdk, type UpdateAgentProfileRequest } from '@omnara/sdk'
import {
  getAgentProfileOptions,
  getAgentProfileQueryKey,
  listAgentProfilesInfiniteOptions,
  listAgentProfilesQueryKey,
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

export function useCreateAgentProfile(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createAgentProfile,
    { orgID, projectID },
    {
      onSuccess: async () => {
        await queryClient.invalidateQueries({
          queryKey: listAgentProfilesQueryKey({ path: { orgID, projectID }, client }),
        })
      },
    },
  )
}

export function useCreateSlackSetup(orgID: string, projectID: string, agentProfileID: string) {
  return useScopedMutation(sdk.createSlackSetup, { orgID, projectID, agentProfileID })
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
    onSuccess: async (_data, { agentProfileID }) => {
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
      queryClient.removeQueries({
        queryKey: getAgentProfileQueryKey({
          path: { orgID, projectID, agentProfileID },
          client,
        }),
        type: 'inactive',
      })
      await queryClient.invalidateQueries({
        queryKey: listAgentProfilesQueryKey({ path: { orgID, projectID }, client }),
      })
    },
  })
}

export function useRemoveAgentProfileQuery(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return (agentProfileID: string) => {
    queryClient.removeQueries({
      queryKey: getAgentProfileQueryKey({ path: { orgID, projectID, agentProfileID }, client }),
    })
  }
}
