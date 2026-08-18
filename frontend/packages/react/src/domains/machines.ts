import {
  type ListVisibleMachinesData,
  type ListVisibleProjectMachinesData,
  type Machine,
  sdk,
} from '@omnara/sdk'
import {
  getMachineOptions,
  listProjectMachineGrantsQueryKey,
  listVisibleMachinesInfiniteOptions,
  listVisibleMachinesQueryKey,
  listVisibleProjectMachinesInfiniteOptions,
  listVisibleProjectMachinesQueryKey,
} from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import {
  type ListFilters,
  type ListSort,
  type PaginatedListOptions,
  paginatedListOptions,
} from './list-options'
import { cursorPagination } from './pagination'

export type MachineListFilters = ListFilters<ListVisibleMachinesData>
export type MachineListSort = ListSort<ListVisibleMachinesData>
export type MachineListOptions = PaginatedListOptions<ListVisibleMachinesData>
export type ProjectMachineListFilters = ListFilters<ListVisibleProjectMachinesData>
export type ProjectMachineListSort = ListSort<ListVisibleProjectMachinesData>
export type ProjectMachineListOptions = PaginatedListOptions<ListVisibleProjectMachinesData>

export function useMachines(orgID: string, options?: MachineListOptions) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListVisibleMachinesData>(options)
  return useInfiniteQuery({
    ...listVisibleMachinesInfiniteOptions({
      path: { orgID },
      query: list.query,
      client,
    }),
    ...cursorPagination,
    enabled: list.enabled,
  })
}

export function useMachine(
  orgID: string,
  machineID: string,
  options?: { refetchInterval?: (machine: Machine | undefined) => number | false },
) {
  const client = useOmnaraClient()
  const refetchInterval = options?.refetchInterval
  return useQuery({
    ...getMachineOptions({ path: { orgID, machineID }, client }),
    refetchInterval:
      refetchInterval == null ? undefined : (query) => refetchInterval(query.state.data),
  })
}

export function useProjectMachines(
  orgID: string,
  projectID: string,
  options?: ProjectMachineListOptions,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListVisibleProjectMachinesData>(options)
  return useInfiniteQuery({
    ...listVisibleProjectMachinesInfiniteOptions({
      path: { orgID, projectID },
      query: list.query,
      client,
    }),
    ...cursorPagination,
    enabled: list.enabled,
  })
}

export interface ConnectMachineInput {
  displayName: string
  projectIDs?: string[]
}

export function useConnectMachine(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ displayName, projectIDs = [] }: ConnectMachineInput) => {
      const { data } = await sdk.connectByoMachine({
        client,
        path: { orgID },
        body: { display_name: displayName, project_ids: projectIDs },
      })
      return data
    },
    onSuccess: async (_data, { projectIDs = [] }) => {
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: listVisibleMachinesQueryKey({ path: { orgID }, client }),
        }),
        ...projectIDs.flatMap((projectID) => [
          queryClient.invalidateQueries({
            queryKey: listVisibleProjectMachinesQueryKey({ path: { orgID, projectID }, client }),
          }),
          queryClient.invalidateQueries({
            queryKey: listProjectMachineGrantsQueryKey({ path: { orgID, projectID }, client }),
          }),
        ]),
      ])
    },
  })
}

export function useDeleteMachine(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (machineID: string) => {
      const { data } = await sdk.deleteMachine({ path: { orgID, machineID }, client })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listVisibleMachinesQueryKey({ path: { orgID }, client }),
      })
    },
  })
}

export function useGrantMachineToProject(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ projectID, machineID }: { projectID: string; machineID: string }) => {
      const { data } = await sdk.createProjectMachineGrant({
        path: { orgID, projectID },
        body: { machine_id: machineID },
        client,
      })
      return data
    },
    onSuccess: async (_data, { projectID }) => {
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: listVisibleProjectMachinesQueryKey({ path: { orgID, projectID }, client }),
        }),
        queryClient.invalidateQueries({
          queryKey: listProjectMachineGrantsQueryKey({ path: { orgID, projectID }, client }),
        }),
      ])
    },
  })
}
