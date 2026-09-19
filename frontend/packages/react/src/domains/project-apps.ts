import { type ListProjectAppsData, type SaveProjectAppRequest, sdk } from '@omnara/sdk'
import { listProjectAppsInfiniteOptions, listProjectAppsQueryKey } from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { type PaginatedListOptions, paginatedListOptions } from './list-options'
import { cursorPaginated } from './pagination'
import { useScopedMutation } from './scoped-mutation'

export function useCreateProjectApp(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createProjectApp,
    { orgID, projectID },
    {
      onSuccess: async () => {
        await queryClient.invalidateQueries({
          queryKey: listProjectAppsQueryKey({ path: { orgID, projectID }, client }),
        })
      },
    },
  )
}

export function useUpdateProjectApp(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ appID, ...body }: SaveProjectAppRequest & { appID: string }) => {
      const { data } = await sdk.updateProjectApp({
        path: { orgID, projectID, appID },
        body,
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listProjectAppsQueryKey({ path: { orgID, projectID }, client }),
      })
    },
  })
}

export function useProjectApps(
  orgID: string,
  projectID: string,
  options?: PaginatedListOptions<ListProjectAppsData>,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListProjectAppsData>(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listProjectAppsInfiniteOptions({ path: { orgID, projectID }, query: list.query, client }),
    ),
    enabled: list.enabled,
  })
}

export function useDeleteProjectApp(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (appID: string) => {
      const { data } = await sdk.deleteProjectApp({ path: { orgID, projectID, appID }, client })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listProjectAppsQueryKey({ path: { orgID, projectID }, client }),
      })
    },
  })
}
