import { type ListCronTriggersData, sdk, type UpdateCronTriggerRequest } from '@omnara/sdk'
import { listCronTriggersInfiniteOptions, listCronTriggersQueryKey } from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import {
  type ListFilters,
  type ListSort,
  type PaginatedListOptions,
  paginatedListOptions,
} from './list-options'
import { cursorPaginated } from './pagination'
import { useScopedMutation } from './scoped-mutation'

export type CronTriggerListFilters = ListFilters<ListCronTriggersData>
export type CronTriggerListSort = ListSort<ListCronTriggersData>
export type CronTriggerListOptions = PaginatedListOptions<ListCronTriggersData>

export function useCronTriggers(
  orgID: string,
  projectID: string,
  options?: CronTriggerListOptions,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListCronTriggersData>(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listCronTriggersInfiniteOptions({
        path: { orgID, projectID },
        query: list.query,
        client,
      }),
    ),
    enabled: list.enabled,
  })
}

export function useCreateCronTrigger(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createCronTrigger,
    { orgID, projectID },
    {
      onSuccess: async () => {
        await queryClient.invalidateQueries({
          queryKey: listCronTriggersQueryKey({ path: { orgID, projectID }, client }),
        })
      },
    },
  )
}

export function useUpdateCronTrigger(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      cronTriggerID,
      ...body
    }: UpdateCronTriggerRequest & { cronTriggerID: string }) => {
      const { data } = await sdk.updateCronTrigger({
        path: { orgID, projectID, cronTriggerID },
        body,
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listCronTriggersQueryKey({ path: { orgID, projectID }, client }),
      })
    },
  })
}

export function useDeleteCronTrigger(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (cronTriggerID: string) => {
      const { data } = await sdk.deleteCronTrigger({
        path: { orgID, projectID, cronTriggerID },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listCronTriggersQueryKey({ path: { orgID, projectID }, client }),
      })
    },
  })
}
