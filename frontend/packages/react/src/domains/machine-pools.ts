import { type ListMachinePoolsData, sdk, type UpdateMachinePoolRequest } from '@omnara/sdk'
import { listMachinePoolsInfiniteOptions, listMachinePoolsQueryKey } from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import {
  type ListFilters,
  type ListSort,
  type PaginatedListOptions,
  paginatedListOptions,
} from './list-options'
import { cursorPagination } from './pagination'
import { useScopedMutation } from './scoped-mutation'

export type MachinePoolListFilters = ListFilters<ListMachinePoolsData>
export type MachinePoolListSort = ListSort<ListMachinePoolsData>
export type MachinePoolListOptions = PaginatedListOptions<ListMachinePoolsData>

export function useMachinePools(orgID: string, options?: MachinePoolListOptions) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListMachinePoolsData>(options)
  return useInfiniteQuery({
    ...listMachinePoolsInfiniteOptions({
      path: { orgID },
      query: list.query,
      client,
    }),
    ...cursorPagination,
    enabled: list.enabled,
  })
}

export function useCreateMachinePool(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createMachinePool,
    { orgID },
    {
      onSuccess: async () => {
        await queryClient.invalidateQueries({
          queryKey: listMachinePoolsQueryKey({ path: { orgID }, client }),
        })
      },
    },
  )
}

export function useUpdateMachinePool(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ poolID, ...body }: UpdateMachinePoolRequest & { poolID: string }) => {
      const { data } = await sdk.updateMachinePool({ path: { orgID, poolID }, body, client })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listMachinePoolsQueryKey({ path: { orgID }, client }),
      })
    },
  })
}

export function useDeleteMachinePool(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (poolID: string) => {
      const { data } = await sdk.deleteMachinePool({ path: { orgID, poolID }, client })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listMachinePoolsQueryKey({ path: { orgID }, client }),
      })
    },
  })
}
