import { sdk } from '@omnara/sdk'
import {
  getOrgOverviewQueryKey,
  listVisibleProjectsInfiniteOptions,
  listVisibleProjectsQueryKey,
} from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useQueryClient, useSuspenseInfiniteQuery } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { cursorPaginated } from './pagination'
import { useScopedMutation } from './scoped-mutation'

export function useProjects(orgID: string) {
  const client = useOmnaraClient()
  return useSuspenseInfiniteQuery(
    cursorPaginated(listVisibleProjectsInfiniteOptions({ path: { orgID }, client })),
  )
}

export function useVisibleProjectsList(orgID: string, options?: { enabled?: boolean }) {
  const client = useOmnaraClient()
  return useInfiniteQuery({
    ...cursorPaginated(listVisibleProjectsInfiniteOptions({ path: { orgID }, client })),
    enabled: options?.enabled ?? true,
  })
}

export function useCreateProject(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createProject,
    { orgID },
    {
      onSuccess: async () => {
        await Promise.all([
          queryClient.invalidateQueries({
            queryKey: listVisibleProjectsQueryKey({ path: { orgID }, client }),
          }),
          queryClient.invalidateQueries({
            queryKey: getOrgOverviewQueryKey({ path: { orgID }, client }),
          }),
        ])
      },
    },
  )
}
