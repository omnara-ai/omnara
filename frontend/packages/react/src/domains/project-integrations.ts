import {
  ApiError,
  type ConfigureProjectIntegrationRequest,
  type CreateGitHubSetupRequest,
  type CreateIntegrationOAuthSetupRequest,
  type CreateSlackSetupRequest,
  type InspectGitHubInstallationsRequest,
  type ListProjectIntegrationsData,
  type ProjectIntegration,
  type SaveProjectIntegrationRequest,
  sdk,
} from '@omnara/sdk'
import {
  getProjectIntegrationOptions,
  getProjectIntegrationQueryKey,
  listAgentsQueryKey,
  listCronTriggersQueryKey,
  listIntegrationDefinitionsOptions,
  listProjectIntegrationsInfiniteOptions,
  listProjectIntegrationsQueryKey,
} from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { type PaginatedListOptions, paginatedListOptions } from './list-options'
import { cursorPaginated } from './pagination'
import { useScopedMutation } from './scoped-mutation'

export function useProjectIntegration(orgID: string, projectID: string, integrationID: string) {
  const client = useOmnaraClient()
  return useQuery({
    ...getProjectIntegrationOptions({ path: { orgID, projectID, integrationID }, client }),
    enabled: integrationID !== '',
    // OAuth setup and settings can change in another tab, even while this read is fresh.
    refetchOnWindowFocus: 'always',
  })
}

export function useCreateProjectIntegration(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createProjectIntegration,
    { orgID, projectID },
    {
      onSuccess: async (integration) => {
        queryClient.setQueryData(
          getProjectIntegrationQueryKey({
            path: { orgID, projectID, integrationID: integration.id },
            client,
          }),
          integration,
        )
        await queryClient.invalidateQueries({
          queryKey: listProjectIntegrationsQueryKey({ path: { orgID, projectID }, client }),
        })
      },
    },
  )
}

export function useUpdateProjectIntegration(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      integrationID,
      ...body
    }: SaveProjectIntegrationRequest & { integrationID: string }) => {
      const { data } = await sdk.updateProjectIntegration({
        path: { orgID, projectID, integrationID },
        body,
        client,
      })
      return data
    },
    onSuccess: async (integration, { integrationID }) => {
      const queryKey = getProjectIntegrationQueryKey({
        path: { orgID, projectID, integrationID },
        client,
      })
      await queryClient.cancelQueries({ queryKey })
      queryClient.setQueryData<ProjectIntegration>(queryKey, (previous) =>
        previous?.setup_revision === integration.setup_revision &&
        previous.state === integration.state
          ? { ...integration, runtime_failure: previous.runtime_failure }
          : integration,
      )
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey,
        }),
        queryClient.invalidateQueries({
          queryKey: listProjectIntegrationsQueryKey({ path: { orgID, projectID }, client }),
        }),
      ])
    },
  })
}

export function useProjectIntegrations(
  orgID: string,
  projectID: string,
  options?: PaginatedListOptions<ListProjectIntegrationsData>,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListProjectIntegrationsData>(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listProjectIntegrationsInfiniteOptions({
        path: { orgID, projectID },
        query: list.query,
        client,
      }),
    ),
    enabled: list.enabled,
  })
}

export function useDeleteProjectIntegration(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (integrationID: string) => {
      const { data } = await sdk.deleteProjectIntegration({
        path: { orgID, projectID, integrationID },
        client,
      })
      return data
    },
    onSuccess: async (_, integrationID) => {
      const queryKey = getProjectIntegrationQueryKey({
        path: { orgID, projectID, integrationID },
        client,
      })
      await queryClient.cancelQueries({ queryKey })
      queryClient.removeQueries({ queryKey })
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: listProjectIntegrationsQueryKey({ path: { orgID, projectID }, client }),
        }),
        queryClient.invalidateQueries({
          queryKey: listAgentsQueryKey({ path: { orgID, projectID }, client }),
        }),
        queryClient.invalidateQueries({
          queryKey: listCronTriggersQueryKey({ path: { orgID, projectID }, client }),
        }),
      ])
    },
  })
}

export function useIntegrationDefinitions(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  return useQuery(listIntegrationDefinitionsOptions({ path: { orgID, projectID }, client }))
}

export function useCreateProjectIntegrationOAuthSetup(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  return useMutation({
    mutationFn: async ({
      integrationID,
      ...body
    }: CreateIntegrationOAuthSetupRequest & { integrationID: string }) => {
      const { data } = await sdk.createProjectIntegrationOAuthSetup({
        path: { orgID, projectID, integrationID },
        body,
        client,
      })
      return data
    },
  })
}

export function useCreateProjectIntegrationSlackSetup(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  // eslint-disable-next-line react-doctor/query-mutation-missing-invalidation -- Only starts OAuth; the integration changes when OAuth completes.
  return useMutation({
    mutationFn: async ({
      integrationID,
      ...body
    }: CreateSlackSetupRequest & { integrationID: string }) => {
      const { data } = await sdk.createProjectIntegrationSlackSetup({
        path: { orgID, projectID, integrationID },
        body,
        client,
      })
      return data
    },
  })
}

export function useCreateProjectIntegrationGitHubSetup(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const cache = useQueryClient()
  return useMutation({
    mutationFn: async ({
      integrationID,
      ...body
    }: CreateGitHubSetupRequest & { integrationID: string }) => {
      const { data } = await sdk.createProjectIntegrationGitHubSetup({
        path: { orgID, projectID, integrationID },
        body,
        client,
      })
      return data
    },
    onError: async (_, { integrationID }) => {
      await cache.invalidateQueries({
        queryKey: getProjectIntegrationQueryKey({
          path: { orgID, projectID, integrationID },
          client,
        }),
      })
    },
  })
}

export function useInspectProjectIntegrationGitHubInstallations(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  // eslint-disable-next-line react-doctor/query-mutation-missing-invalidation -- Reads GitHub; no Omnara state changes.
  return useMutation({
    mutationFn: async ({
      integrationID,
      ...body
    }: InspectGitHubInstallationsRequest & { integrationID: string }) => {
      const { data } = await sdk.inspectProjectIntegrationGitHubInstallations({
        path: { orgID, projectID, integrationID },
        body,
        client,
      })
      return data
    },
  })
}

export function useConfigureProjectIntegration(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const cache = useQueryClient()
  return useMutation({
    mutationFn: async ({
      integrationID,
      ...body
    }: ConfigureProjectIntegrationRequest & { integrationID: string }) => {
      const { data } = await sdk.configureProjectIntegration({
        path: { orgID, projectID, integrationID },
        body,
        client,
      })
      return data
    },
    onSuccess: async (integration) => {
      const queryKey = getProjectIntegrationQueryKey({
        path: { orgID, projectID, integrationID: integration.id },
        client,
      })
      await cache.cancelQueries({ queryKey })
      cache.setQueryData(queryKey, integration)
      await cache.invalidateQueries({
        queryKey: listProjectIntegrationsQueryKey({ path: { orgID, projectID }, client }),
      })
    },
    onError: async (_, { integrationID }) => {
      await cache.invalidateQueries({
        queryKey: getProjectIntegrationQueryKey({
          path: { orgID, projectID, integrationID },
          client,
        }),
      })
    },
  })
}

export function useDisconnectProjectIntegration(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const cache = useQueryClient()
  return useMutation({
    mutationFn: async (integrationID: string) => {
      const { data } = await sdk.disconnectProjectIntegration({
        path: { orgID, projectID, integrationID },
        client,
      })
      return data
    },
    onSuccess: async (integration, integrationID) => {
      const queryKey = getProjectIntegrationQueryKey({
        path: { orgID, projectID, integrationID },
        client,
      })
      await cache.cancelQueries({ queryKey })
      cache.setQueryData(queryKey, integration)
      await Promise.all([
        cache.invalidateQueries({
          queryKey: listProjectIntegrationsQueryKey({ path: { orgID, projectID }, client }),
        }),
        cache.invalidateQueries({
          queryKey: listAgentsQueryKey({ path: { orgID, projectID }, client }),
        }),
      ])
    },
  })
}

export function useProjectIntegrationOAuthCompletion(
  orgID: string,
  projectID: string,
  integrationID: string,
  flow?: { flow_id: string; expires_at: string; setup_revision: number },
) {
  const client = useOmnaraClient()
  return useQuery({
    ...getProjectIntegrationOptions({ path: { orgID, projectID, integrationID }, client }),
    enabled: Boolean(flow) && integrationID !== '',
    refetchInterval: (query) =>
      !flow ||
      Date.now() >= Date.parse(flow.expires_at) ||
      (query.state.error instanceof ApiError && query.state.error.status === 404) ||
      (query.state.data !== undefined && query.state.data.setup_revision > flow.setup_revision) ||
      (query.state.data?.state === 'active' && query.state.data.last_oauth_flow_id === flow.flow_id)
        ? false
        : 2000,
  })
}
