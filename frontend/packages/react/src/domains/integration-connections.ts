import { sdk } from '@omnara/sdk'
import {
  getIntegrationLaunchProfileOptions,
  getIntegrationLaunchProfileQueryKey,
  listGitHubSetupInstallationsInfiniteOptions,
  listGitHubSetupRepositoriesInfiniteOptions,
  listIntegrationInstallsQueryKey,
} from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'

import { useOmnaraClient } from '../omnara-client'
import { useScopedMutation } from './scoped-mutation'

export function useIntegrationLaunchProfile(
  orgID: string,
  projectID: string,
  integrationInstallID: string,
) {
  const client = useOmnaraClient()
  return useQuery(
    getIntegrationLaunchProfileOptions({
      path: { orgID, projectID, integrationInstallID },
      client,
    }),
  )
}

export function useSetIntegrationLaunchProfile(
  orgID: string,
  projectID: string,
  integrationInstallID: string,
) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  const path = { orgID, projectID, integrationInstallID }
  const refresh = useRefreshIntegrationConnections(orgID, projectID)
  return useScopedMutation(sdk.setIntegrationLaunchProfile, path, {
    onSuccess: async (data) => {
      queryClient.setQueryData(getIntegrationLaunchProfileQueryKey({ path, client }), data)
      await refresh()
    },
  })
}

export function useStartIntegrationConnection(orgID: string, projectID: string) {
  return useScopedMutation(sdk.startIntegrationConnection, { orgID, projectID })
}

export function useGitHubSetupInstallations(orgID: string, projectID: string, flowID: string) {
  const client = useOmnaraClient()
  return useInfiniteQuery({
    ...listGitHubSetupInstallationsInfiniteOptions({ path: { orgID, projectID, flowID }, client }),
    initialPageParam: 1,
    getNextPageParam: (page) => page.next_page,
    staleTime: 0,
  })
}

export function useGitHubSetupRepositories(
  orgID: string,
  projectID: string,
  flowID: string,
  providerInstallationID: string,
) {
  const client = useOmnaraClient()
  return useInfiniteQuery({
    ...listGitHubSetupRepositoriesInfiniteOptions({
      path: { orgID, projectID, flowID, providerInstallationID },
      client,
    }),
    initialPageParam: 1,
    getNextPageParam: (page) => page.next_page,
    staleTime: 0,
  })
}

export function useRefreshIntegrationConnections(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useCallback(
    () =>
      queryClient.invalidateQueries({
        queryKey: listIntegrationInstallsQueryKey({ path: { orgID, projectID }, client }),
      }),
    [client, orgID, projectID, queryClient],
  )
}

export function useCompleteGitHubConnection(orgID: string, projectID: string, flowID: string) {
  const refresh = useRefreshIntegrationConnections(orgID, projectID)
  return useScopedMutation(
    sdk.completeGitHubConnection,
    { orgID, projectID, flowID },
    { onSuccess: refresh },
  )
}
