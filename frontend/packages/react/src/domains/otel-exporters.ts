import { sdk, type UpdateOtelExporterRequest } from '@omnara/sdk'
import { listOtelExportersInfiniteOptions } from '@omnara/sdk/tanstack'
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

/** An organization holds at most this many exporters, so one page lists them all. */
const OTEL_EXPORTER_PAGE_SIZE = 100

/** Every exporter in the organization, whatever its scope. Only org admins may list them. */
export function useOtelExporters(orgID: string, options: { enabled?: boolean } = {}) {
  const client = useOmnaraClient()
  return useInfiniteQuery({
    ...cursorPaginated(
      listOtelExportersInfiniteOptions({
        client,
        path: { orgID },
        query: { limit: OTEL_EXPORTER_PAGE_SIZE },
      }),
    ),
    enabled: options.enabled ?? true,
  })
}

export function useCreateOtelExporter(orgID: string) {
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createOtelExporter,
    { orgID },
    {
      onSuccess: async () => {
        await invalidateOtelExporterQueries(queryClient, orgID)
      },
    },
  )
}

export function useUpdateOtelExporter(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      exporterID,
      ...body
    }: UpdateOtelExporterRequest & { exporterID: string }) => {
      const { data } = await sdk.updateOtelExporter({
        client,
        path: { orgID, exporterID },
        body,
      })
      return data
    },
    onSuccess: async () => {
      await invalidateOtelExporterQueries(queryClient, orgID)
    },
  })
}

export function useDeleteOtelExporter(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (exporterID: string) => {
      await sdk.deleteOtelExporter({ client, path: { orgID, exporterID } })
    },
    onSuccess: async () => {
      await invalidateOtelExporterQueries(queryClient, orgID)
    },
  })
}

function invalidateOtelExporterQueries(queryClient: QueryClient, orgID: string) {
  return queryClient.invalidateQueries({
    predicate: (query) => {
      const entry = generatedQueryKey(query)
      return entry?._id === 'listOtelExporters' && entry.path?.orgID === orgID
    },
  })
}
