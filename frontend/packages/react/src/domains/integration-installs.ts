import { type ListIntegrationInstallsData, sdk } from '@omnara/sdk'
import {
  listAgentsQueryKey,
  listIntegrationInstallsInfiniteOptions,
  listIntegrationInstallsQueryKey,
} from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import {
  type ListFilters,
  type ListSort,
  type PaginatedListOptions,
  paginatedListOptions,
} from './list-options'
import { cursorPaginated } from './pagination'

export type IntegrationInstallListFilters = ListFilters<ListIntegrationInstallsData>
export type IntegrationInstallListSort = ListSort<ListIntegrationInstallsData>
export type IntegrationInstallListOptions = PaginatedListOptions<ListIntegrationInstallsData>

export function useIntegrationInstalls(
  orgID: string,
  projectID: string,
  options?: IntegrationInstallListOptions,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListIntegrationInstallsData>(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listIntegrationInstallsInfiniteOptions({
        path: { orgID, projectID },
        query: list.query,
        client,
      }),
    ),
    enabled: list.enabled,
  })
}

export function useDeleteIntegrationInstall(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (integrationInstallID: string) => {
      const { data } = await sdk.deleteIntegrationInstall({
        path: { orgID, projectID, integrationInstallID },
        client,
      })
      return data
    },
    onSuccess: async () => {
      // Deleting an install also clears integration targets from agents.
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: listIntegrationInstallsQueryKey({ path: { orgID, projectID }, client }),
        }),
        queryClient.invalidateQueries({
          queryKey: listAgentsQueryKey({ path: { orgID, projectID }, client }),
        }),
      ])
    },
  })
}
