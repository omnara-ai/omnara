import { type ListVisibleMachinesData, type ListVisibleProjectMachinesData, sdk } from '@omnara/sdk'
import {
  listVisibleMachinesInfiniteOptions,
  listVisibleMachinesQueryKey,
  listVisibleProjectMachinesInfiniteOptions,
} from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'

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

/**
 * Connect a BYO machine: create it, optionally grant it to projects, and mint
 * a machine token for the connection instructions. Grant failures are returned
 * separately so the machine token, which is shown once, is never lost.
 */
export function useConnectMachine(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ displayName, projectIDs = [] }: ConnectMachineInput) => {
      const { data: machine } = await sdk.createMachine({
        client,
        path: { orgID },
        body: { display_name: displayName },
      })
      const { data: token } = await sdk.createByoMachineDaemonToken({
        client,
        path: { orgID, machineID: machine.id },
        body: { name: 'web-console' },
      })
      const grantResults = await Promise.allSettled(
        projectIDs.map((projectID) =>
          sdk.createProjectMachineGrant({
            client,
            path: { orgID, projectID },
            body: { machine_id: machine.id },
          }),
        ),
      )
      const failedProjectGrants = grantResults.flatMap((result, index) => {
        const projectID = projectIDs[index]
        if (result.status !== 'rejected' || projectID === undefined) return []
        const reason: unknown = result.reason
        return [{ projectID, message: reason instanceof Error ? reason.message : '' }]
      })
      return { machine, token, failedProjectGrants }
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listVisibleMachinesQueryKey({ path: { orgID }, client }),
      })
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
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        predicate: (query) => {
          const entry = query.queryKey[0] as { _id?: string; path?: { orgID?: string } } | undefined
          return entry?._id === 'listProjectMachineGrants' && entry.path?.orgID === orgID
        },
      })
    },
  })
}
