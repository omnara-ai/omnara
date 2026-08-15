import { sdk } from '@omnara/sdk'
import {
  getOrgOverviewQueryKey,
  listVisibleProjectsInfiniteOptions,
  listVisibleProjectsQueryKey,
} from '@omnara/sdk/tanstack'
import {
  type SkipToken,
  useInfiniteQuery,
  useQueryClient,
  useSuspenseInfiniteQuery,
} from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { cursorPagination } from './pagination'
import { useScopedMutation } from './scoped-mutation'

export function useProjects(orgID: string) {
  const client = useOmnaraClient()
  const options = listVisibleProjectsInfiniteOptions({ path: { orgID }, client })
  return useSuspenseInfiniteQuery({
    ...options,
    // The generated queryFn type includes skipToken, which the suspense
    // variant forbids; the generated builder never actually emits it.
    queryFn: options.queryFn as Exclude<typeof options.queryFn, SkipToken | undefined>,
    ...cursorPagination,
  })
}

export function useVisibleProjectsList(orgID: string, options?: { enabled?: boolean }) {
  const client = useOmnaraClient()
  return useInfiniteQuery({
    ...listVisibleProjectsInfiniteOptions({ path: { orgID }, client }),
    ...cursorPagination,
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
