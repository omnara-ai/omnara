import { type ListAppSubscriptionsData, type ListAppSubscriptionsResponse, sdk } from '@omnara/sdk'
import {
  listAppSubscriptionsInfiniteOptions,
  listAppSubscriptionsInfiniteQueryKey,
  listAppSubscriptionsQueryKey,
} from '@omnara/sdk/tanstack'
import {
  type InfiniteData,
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { type PaginatedListOptions, paginatedListOptions } from './list-options'
import { cursorPaginated } from './pagination'
import { useScopedMutation } from './scoped-mutation'

export function useAppSubscriptions(
  orgID: string,
  projectID: string,
  appID: string,
  options?: PaginatedListOptions<ListAppSubscriptionsData>,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListAppSubscriptionsData>(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listAppSubscriptionsInfiniteOptions({
        path: { orgID, projectID, appID },
        query: list.query,
        client,
      }),
    ),
    enabled: appID !== '' && list.enabled,
  })
}

export function useCreateAppSubscription(orgID: string, projectID: string, appID: string) {
  const client = useOmnaraClient()
  const cache = useQueryClient()
  return useScopedMutation(
    sdk.createAppSubscription,
    { orgID, projectID, appID },
    {
      onSuccess: async () => {
        await cache.invalidateQueries({
          queryKey: listAppSubscriptionsQueryKey({ path: { orgID, projectID, appID }, client }),
        })
      },
    },
  )
}

export function useDeleteAppSubscription(orgID: string, projectID: string, appID: string) {
  const client = useOmnaraClient()
  const cache = useQueryClient()
  return useMutation({
    mutationFn: async (subscriptionID: string) => {
      await sdk.deleteAppSubscription({ path: { orgID, projectID, appID, subscriptionID }, client })
    },
    onSuccess: async (_, subscriptionID) => {
      const queryKey = listAppSubscriptionsQueryKey({ path: { orgID, projectID, appID }, client })
      await cache.cancelQueries({ queryKey })
      // A failed refresh must not leave a successfully detached conversation on screen.
      cache.setQueriesData<InfiniteData<ListAppSubscriptionsResponse>>(
        {
          queryKey: listAppSubscriptionsInfiniteQueryKey({
            path: { orgID, projectID, appID },
            client,
          }),
        },
        (data) =>
          data && {
            ...data,
            pages: data.pages.map((page) => ({
              ...page,
              data: page.data.filter((subscription) => subscription.id !== subscriptionID),
            })),
          },
      )
      await cache.invalidateQueries({ queryKey })
    },
  })
}
