import { type CreateOtelExporterRequest, sdk, type UpdateOtelExporterRequest } from '@omnara/sdk'
import {
  listOrgOtelExportersInfiniteOptions,
  listOtelExportersInfiniteOptions,
} from '@omnara/sdk/tanstack'
import {
  type QueryClient,
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { cursorPaginated } from './pagination'
import { generatedQueryKey } from './query-keys'

/** A project holds at most this many exporters, so one page lists a project's. */
const OTEL_EXPORTER_PAGE_SIZE = 100

/** One project's exporters. */
export function useOtelExporters(
  orgID: string,
  projectID: string,
  options: { enabled?: boolean } = {},
) {
  const client = useOmnaraClient()
  return useInfiniteQuery({
    ...cursorPaginated(
      listOtelExportersInfiniteOptions({
        client,
        path: { orgID, projectID },
        query: { limit: OTEL_EXPORTER_PAGE_SIZE },
      }),
    ),
    enabled: (options.enabled ?? true) && projectID !== '',
  })
}

/** The exporters of every project in the organization that the viewer can read. */
export function useOrgOtelExporters(orgID: string, options: { enabled?: boolean } = {}) {
  const client = useOmnaraClient()
  return useInfiniteQuery({
    ...cursorPaginated(
      listOrgOtelExportersInfiniteOptions({
        client,
        path: { orgID },
        query: { limit: OTEL_EXPORTER_PAGE_SIZE },
      }),
    ),
    enabled: options.enabled ?? true,
  })
}

export function useCreateOtelExporter(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      projectID,
      ...body
    }: CreateOtelExporterRequest & { projectID: string }) => {
      const { data } = await sdk.createOtelExporter({ client, path: { orgID, projectID }, body })
      return data
    },
    onSuccess: async () => {
      await invalidateOtelExporterQueries(queryClient, orgID)
    },
  })
}

export function useUpdateOtelExporter(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      projectID,
      exporterID,
      ...body
    }: UpdateOtelExporterRequest & { projectID: string; exporterID: string }) => {
      const { data } = await sdk.updateOtelExporter({
        client,
        path: { orgID, projectID, exporterID },
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
    mutationFn: async ({ projectID, exporterID }: { projectID: string; exporterID: string }) => {
      await sdk.deleteOtelExporter({ client, path: { orgID, projectID, exporterID } })
    },
    onSuccess: async () => {
      await invalidateOtelExporterQueries(queryClient, orgID)
    },
  })
}

/** A change in one project shows in both that project's list and the organization's. */
function invalidateOtelExporterQueries(queryClient: QueryClient, orgID: string) {
  return queryClient.invalidateQueries({
    predicate: (query) => {
      const entry = generatedQueryKey(query)
      return (
        (entry?._id === 'listOtelExporters' || entry?._id === 'listOrgOtelExporters') &&
        entry.path?.orgID === orgID
      )
    },
  })
}
