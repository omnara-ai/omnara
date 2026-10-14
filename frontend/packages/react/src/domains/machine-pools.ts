import {
  type CheckMachinePoolConnectionRequest,
  type ListMachinePoolsData,
  sdk,
  type UpdateMachinePoolRequest,
} from '@omnara/sdk'
import {
  getMachinePoolOptions,
  listMachinePoolsInfiniteOptions,
  listMachinePoolsQueryKey,
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

export type MachinePoolListFilters = ListFilters<ListMachinePoolsData>
export type MachinePoolListSort = ListSort<ListMachinePoolsData>
export type MachinePoolListOptions = PaginatedListOptions<ListMachinePoolsData>

export function useMachinePools(orgID: string, options?: MachinePoolListOptions) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListMachinePoolsData>(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listMachinePoolsInfiniteOptions({
        path: { orgID },
        query: list.query,
        client,
      }),
    ),
    enabled: list.enabled,
  })
}

export function useMachinePool(orgID: string, poolID: string, options?: { enabled?: boolean }) {
  const client = useOmnaraClient()
  return useQuery({
    ...getMachinePoolOptions({ path: { orgID, poolID }, client }),
    enabled: (options?.enabled ?? true) && poolID !== '',
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

export function useMachinePoolConnectionCheck(
  orgID: string,
  request: CheckMachinePoolConnectionRequest | null,
) {
  const client = useOmnaraClient()
  return useQuery({
    queryKey: ['machine-pool-connection-check', orgID, request],
    queryFn: async ({ signal }) => {
      if (request == null) throw new Error('machine pool connection check request is required')
      await sdk.checkMachinePoolConnection({ path: { orgID }, body: request, client, signal })
      return null
    },
    enabled: request != null && orgID !== '',
    staleTime: 60 * 1000,
    retry: false,
  })
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
