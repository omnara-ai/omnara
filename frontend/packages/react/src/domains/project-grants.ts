import {
  type CreateProjectMachinePoolGrantRequest,
  type CreateProjectModelGrantRequest,
  type ListProjectMachineGrantsData,
  type ListProjectMachinePoolGrantsData,
  type ListProjectModelGrantsData,
  sdk,
  type UpdateProjectMachinePoolGrantRequest,
  type UpdateProjectModelGrantRequest,
} from '@omnara/sdk'
import {
  listProjectMachineGrantsInfiniteOptions,
  listProjectMachineGrantsQueryKey,
  listProjectMachinePoolGrantsInfiniteOptions,
  listProjectMachinePoolGrantsQueryKey,
  listProjectModelGrantsInfiniteOptions,
  listProjectModelGrantsQueryKey,
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
import { useScopedMutation } from './scoped-mutation'

export type ProjectMachinePoolGrantListFilters = ListFilters<ListProjectMachinePoolGrantsData>
export type ProjectMachinePoolGrantListSort = ListSort<ListProjectMachinePoolGrantsData>
export type ProjectMachinePoolGrantListOptions =
  PaginatedListOptions<ListProjectMachinePoolGrantsData>

export function useProjectMachinePoolGrants(
  orgID: string,
  projectID: string,
  options?: ProjectMachinePoolGrantListOptions,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListProjectMachinePoolGrantsData>(options)
  return useInfiniteQuery({
    ...listProjectMachinePoolGrantsInfiniteOptions({
      path: { orgID, projectID },
      query: list.query,
      client,
    }),
    ...cursorPagination,
    enabled: list.enabled,
  })
}

export type ProjectMachineGrantListFilters = ListFilters<ListProjectMachineGrantsData>
export type ProjectMachineGrantListSort = ListSort<ListProjectMachineGrantsData>
export type ProjectMachineGrantListOptions = PaginatedListOptions<ListProjectMachineGrantsData>

export function useProjectMachineGrants(
  orgID: string,
  projectID: string,
  options?: ProjectMachineGrantListOptions,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListProjectMachineGrantsData>(options)
  return useInfiniteQuery({
    ...listProjectMachineGrantsInfiniteOptions({
      path: { orgID, projectID },
      query: list.query,
      client,
    }),
    ...cursorPagination,
    enabled: list.enabled,
  })
}

export function useDeleteProjectMachineGrant(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (grantID: string) => {
      const { data } = await sdk.deleteProjectMachineGrant({
        path: { orgID, projectID, grantID },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listProjectMachineGrantsQueryKey({ path: { orgID, projectID }, client }),
      })
    },
  })
}

export function useCreateProjectMachinePoolGrant(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createProjectMachinePoolGrant,
    { orgID, projectID },
    {
      onSuccess: async () => {
        await queryClient.invalidateQueries({
          queryKey: listProjectMachinePoolGrantsQueryKey({ path: { orgID, projectID }, client }),
        })
      },
    },
  )
}

export function useGrantMachinePoolToProject(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      projectID,
      ...body
    }: CreateProjectMachinePoolGrantRequest & { projectID: string }) => {
      const { data } = await sdk.createProjectMachinePoolGrant({
        path: { orgID, projectID },
        body,
        client,
      })
      return data
    },
    onSuccess: async (_data, variables) => {
      await queryClient.invalidateQueries({
        queryKey: listProjectMachinePoolGrantsQueryKey({
          path: { orgID, projectID: variables.projectID },
          client,
        }),
      })
    },
  })
}

export function useUpdateProjectMachinePoolGrant(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      poolGrantID,
      ...body
    }: UpdateProjectMachinePoolGrantRequest & { poolGrantID: string }) => {
      const { data } = await sdk.updateProjectMachinePoolGrant({
        path: { orgID, projectID, poolGrantID },
        body,
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listProjectMachinePoolGrantsQueryKey({ path: { orgID, projectID }, client }),
      })
    },
  })
}

export function useDeleteProjectMachinePoolGrant(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (poolGrantID: string) => {
      const { data } = await sdk.deleteProjectMachinePoolGrant({
        path: { orgID, projectID, poolGrantID },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listProjectMachinePoolGrantsQueryKey({ path: { orgID, projectID }, client }),
      })
    },
  })
}

export type ProjectModelGrantListFilters = ListFilters<ListProjectModelGrantsData>
export type ProjectModelGrantListSort = ListSort<ListProjectModelGrantsData>
export type ProjectModelGrantListOptions = PaginatedListOptions<ListProjectModelGrantsData>

export function useProjectModelGrants(
  orgID: string,
  projectID: string,
  options?: ProjectModelGrantListOptions,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListProjectModelGrantsData>(options)
  return useInfiniteQuery({
    ...listProjectModelGrantsInfiniteOptions({
      path: { orgID, projectID },
      query: list.query,
      client,
    }),
    ...cursorPagination,
    enabled: list.enabled,
  })
}

/**
 * The target project is picked inside the grant dialog, so it stays a
 * mutation variable rather than hook scope.
 */
export function useCreateProjectModelGrant(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      projectID,
      ...body
    }: CreateProjectModelGrantRequest & { projectID: string }) => {
      const { data } = await sdk.createProjectModelGrant({
        path: { orgID, projectID },
        body,
        client,
      })
      return data
    },
    onSuccess: async (_data, variables) => {
      await queryClient.invalidateQueries({
        queryKey: listProjectModelGrantsQueryKey({
          path: { orgID, projectID: variables.projectID },
          client,
        }),
      })
    },
  })
}

export function useUpdateProjectModelGrant(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      modelGrantID,
      ...body
    }: UpdateProjectModelGrantRequest & { modelGrantID: string }) => {
      const { data } = await sdk.updateProjectModelGrant({
        path: { orgID, projectID, modelGrantID },
        body,
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listProjectModelGrantsQueryKey({ path: { orgID, projectID }, client }),
      })
    },
  })
}

export function useDeleteProjectModelGrant(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (modelGrantID: string) => {
      const { data } = await sdk.deleteProjectModelGrant({
        path: { orgID, projectID, modelGrantID },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listProjectModelGrantsQueryKey({ path: { orgID, projectID }, client }),
      })
    },
  })
}
