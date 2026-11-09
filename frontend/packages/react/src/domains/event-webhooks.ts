import { sdk, type UpdateEventWebhookRequest } from '@omnara/sdk'
import { listEventWebhooksInfiniteOptions } from '@omnara/sdk/tanstack'
import {
  type QueryClient,
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { cursorPaginated } from './pagination'
import { generatedQueryKey } from './query-keys'
import { useScopedMutation } from './scoped-mutation'

export function useEventWebhooks(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  return useInfiniteQuery(
    cursorPaginated(
      listEventWebhooksInfiniteOptions({
        client,
        path: { orgID, projectID },
      }),
    ),
  )
}

export function useCreateEventWebhook(orgID: string, projectID: string) {
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createEventWebhook,
    { orgID, projectID },
    {
      onSuccess: async () => {
        await invalidateEventWebhookQueries(queryClient, orgID, projectID)
      },
    },
  )
}

export function useUpdateEventWebhook(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      webhookID,
      ...body
    }: UpdateEventWebhookRequest & { webhookID: string }) => {
      const { data } = await sdk.updateEventWebhook({
        client,
        path: { orgID, projectID, webhookID },
        body,
      })
      return data
    },
    onSuccess: async () => {
      await invalidateEventWebhookQueries(queryClient, orgID, projectID)
    },
  })
}

export function useDeleteEventWebhook(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (webhookID: string) => {
      await sdk.deleteEventWebhook({ client, path: { orgID, projectID, webhookID } })
    },
    onSuccess: async () => {
      await invalidateEventWebhookQueries(queryClient, orgID, projectID)
    },
  })
}

function invalidateEventWebhookQueries(queryClient: QueryClient, orgID: string, projectID: string) {
  return queryClient.invalidateQueries({
    predicate: (query) => {
      const entry = generatedQueryKey(query)
      return (
        entry?._id === 'listEventWebhooks' &&
        entry.path?.orgID === orgID &&
        entry.path.projectID === projectID
      )
    },
  })
}
