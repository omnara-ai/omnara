import {
  type ListIntegrationConnectionsData,
  type SaveIntegrationConnectionRequest,
  sdk,
} from '@omnara/sdk'
import {
  getIntegrationConnectionOptions,
  getIntegrationConnectionQueryKey,
  listAgentsQueryKey,
  listIntegrationConnectionsInfiniteOptions,
  listIntegrationConnectionsQueryKey,
} from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import {
  type ListFilters,
  type ListSort,
  type PaginatedListOptions,
  paginatedListOptions,
} from './list-options'
import { cursorPaginated } from './pagination'
import { useScopedMutation } from './scoped-mutation'

export function useCreateProjectSlackSetup(orgID: string, projectID: string) {
  return useScopedMutation(sdk.createProjectSlackSetup, { orgID, projectID })
}

export function useCreateProjectIntegrationOAuthSetup(orgID: string, projectID: string) {
  return useScopedMutation(sdk.createProjectIntegrationOAuthSetup, { orgID, projectID })
}

export function useIntegrationConnection(
  orgID: string,
  projectID: string,
  integrationConnectionID: string,
) {
  const client = useOmnaraClient()
  return useQuery({
    ...getIntegrationConnectionOptions({
      path: { orgID, projectID, integrationConnectionID },
      client,
    }),
    enabled: integrationConnectionID !== '',
  })
}

export function useCreateIntegrationConnection(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createIntegrationConnection,
    { orgID, projectID },
    {
      onSuccess: async () => {
        await queryClient.invalidateQueries({
          queryKey: listIntegrationConnectionsQueryKey({ path: { orgID, projectID }, client }),
        })
      },
    },
  )
}

export function useUpdateIntegrationConnection(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      integrationConnectionID,
      ...body
    }: SaveIntegrationConnectionRequest & { integrationConnectionID: string }) => {
      const { data } = await sdk.updateIntegrationConnection({
        path: { orgID, projectID, integrationConnectionID },
        body,
        client,
      })
      return data
    },
    onSuccess: async (connection) => {
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: listIntegrationConnectionsQueryKey({ path: { orgID, projectID }, client }),
        }),
        queryClient.invalidateQueries({
          queryKey: getIntegrationConnectionQueryKey({
            path: { orgID, projectID, integrationConnectionID: connection.id },
            client,
          }),
        }),
      ])
    },
  })
}

export type IntegrationConnectionListFilters = ListFilters<ListIntegrationConnectionsData>
export type IntegrationConnectionListSort = ListSort<ListIntegrationConnectionsData>
export type IntegrationConnectionListOptions = PaginatedListOptions<ListIntegrationConnectionsData>

export function useIntegrationConnections(
  orgID: string,
  projectID: string,
  options?: IntegrationConnectionListOptions,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListIntegrationConnectionsData>(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listIntegrationConnectionsInfiniteOptions({
        path: { orgID, projectID },
        query: list.query,
        client,
      }),
    ),
    enabled: list.enabled,
  })
}

export function useDeleteIntegrationConnection(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (integrationConnectionID: string) => {
      const { data } = await sdk.deleteIntegrationConnection({
        path: { orgID, projectID, integrationConnectionID },
        client,
      })
      return data
    },
    onSuccess: async (_, integrationConnectionID) => {
      // Deleting a connection also clears integration targets from agents.
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: getIntegrationConnectionQueryKey({
            path: { orgID, projectID, integrationConnectionID },
            client,
          }),
        }),
        queryClient.invalidateQueries({
          queryKey: listIntegrationConnectionsQueryKey({ path: { orgID, projectID }, client }),
        }),
        queryClient.invalidateQueries({
          queryKey: listAgentsQueryKey({ path: { orgID, projectID }, client }),
        }),
      ])
    },
  })
}
