import { sdk } from '@omnara/sdk'
import {
  getIntegrationAppOptions,
  listEligibleIntegrationAppsInfiniteOptions,
  listIntegrationAppsInfiniteOptions,
} from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { cursorPaginated, DEFAULT_LIST_PAGE_SIZE } from './pagination'
import { generatedQueryKey } from './query-keys'
import { useScopedMutation } from './scoped-mutation'

export function useIntegrationApps(orgID: string, options?: { enabled?: boolean }) {
  const client = useOmnaraClient()
  return useInfiniteQuery({
    ...cursorPaginated(
      listIntegrationAppsInfiniteOptions({
        path: { orgID },
        query: { limit: DEFAULT_LIST_PAGE_SIZE },
        client,
      }),
    ),
    enabled: options?.enabled ?? true,
  })
}

export function useEligibleIntegrationApps(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  return useInfiniteQuery(
    cursorPaginated(
      listEligibleIntegrationAppsInfiniteOptions({
        path: { orgID, projectID },
        query: { limit: DEFAULT_LIST_PAGE_SIZE },
        client,
      }),
    ),
  )
}

export function useIntegrationApp(orgID: string, integrationAppID: string) {
  const client = useOmnaraClient()
  return useQuery(getIntegrationAppOptions({ path: { orgID, integrationAppID }, client }))
}

function useInvalidateIntegrationApps(orgID: string) {
  const queryClient = useQueryClient()
  return () =>
    queryClient.invalidateQueries({
      predicate: (query) => {
        const entry = generatedQueryKey(query)
        return (
          entry?.path?.orgID === orgID &&
          [
            'listIntegrationApps',
            'getIntegrationApp',
            'listEligibleIntegrationApps',
            'listIntegrationInstalls',
            'listRegisteredChannels',
          ].includes(entry._id)
        )
      },
    })
}

export function useCreateIntegrationApp(orgID: string) {
  const invalidate = useInvalidateIntegrationApps(orgID)
  return useScopedMutation(sdk.createIntegrationApp, { orgID }, { onSuccess: invalidate })
}

export function useUpdateIntegrationApp(orgID: string, integrationAppID: string) {
  const invalidate = useInvalidateIntegrationApps(orgID)
  return useScopedMutation(
    sdk.updateIntegrationApp,
    { orgID, integrationAppID },
    { onSuccess: invalidate },
  )
}
