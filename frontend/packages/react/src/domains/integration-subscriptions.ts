import {
  type ListIntegrationSubscriptionsData,
  type ListIntegrationSubscriptionsResponse,
  sdk,
} from '@omnara/sdk'
import {
  listIntegrationSubscriptionsInfiniteOptions,
  listIntegrationSubscriptionsInfiniteQueryKey,
  listIntegrationSubscriptionsQueryKey,
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

export function useIntegrationSubscriptions(
  orgID: string,
  projectID: string,
  integrationID: string,
  options?: PaginatedListOptions<ListIntegrationSubscriptionsData>,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListIntegrationSubscriptionsData>(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listIntegrationSubscriptionsInfiniteOptions({
        path: { orgID, projectID, integrationID },
        query: list.query,
        client,
      }),
    ),
    enabled: integrationID !== '' && list.enabled,
  })
}

export function useCreateIntegrationSubscription(
  orgID: string,
  projectID: string,
  integrationID: string,
) {
  const client = useOmnaraClient()
  const cache = useQueryClient()
  return useScopedMutation(
    sdk.createIntegrationSubscription,
    { orgID, projectID, integrationID },
    {
      onSuccess: async () => {
        await cache.invalidateQueries({
          queryKey: listIntegrationSubscriptionsQueryKey({
            path: { orgID, projectID, integrationID },
            client,
          }),
        })
      },
    },
  )
}

export function useDeleteIntegrationSubscription(
  orgID: string,
  projectID: string,
  integrationID: string,
) {
  const client = useOmnaraClient()
  const cache = useQueryClient()
  return useMutation({
    mutationFn: async (subscriptionID: string) => {
      await sdk.deleteIntegrationSubscription({
        path: { orgID, projectID, integrationID, subscriptionID },
        client,
      })
    },
    onSuccess: async (_, subscriptionID) => {
      const queryKey = listIntegrationSubscriptionsQueryKey({
        path: { orgID, projectID, integrationID },
        client,
      })
      await cache.cancelQueries({ queryKey })
      cache.setQueriesData<InfiniteData<ListIntegrationSubscriptionsResponse>>(
        {
          queryKey: listIntegrationSubscriptionsInfiniteQueryKey({
            path: { orgID, projectID, integrationID },
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
