import {
  type ConfigureIntegrationRequest,
  type CreateGitHubSetupRequest,
  type CreateIntegrationOAuthSetupRequest,
  type CreateSlackSetupRequest,
  type InspectGitHubInstallationsRequest,
  type Integration,
  type ListIntegrationsData,
  sdk,
  type UpdateIntegrationRequest,
} from '@omnara/sdk'
import {
  getIntegrationOptions,
  getIntegrationQueryKey,
  listAgentsQueryKey,
  listCronTriggersQueryKey,
  listIntegrationDefinitionsOptions,
  listIntegrationsInfiniteOptions,
  listIntegrationsQueryKey,
  listOrgAgentsQueryKey,
} from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { type PaginatedListOptions, paginatedListOptions } from './list-options'
import { cursorPaginated } from './pagination'
import { useScopedMutation } from './scoped-mutation'

export function useIntegration(orgID: string, projectID: string, integrationID: string) {
  const client = useOmnaraClient()
  return useQuery({
    ...getIntegrationOptions({ path: { orgID, projectID, integrationID }, client }),
    enabled: integrationID !== '',
    // OAuth setup and settings can change in another tab, even while this read is fresh.
    refetchOnWindowFocus: 'always',
  })
}

export function useCreateIntegration(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createIntegration,
    { orgID, projectID },
    {
      onSuccess: async (integration) => {
        queryClient.setQueryData(
          getIntegrationQueryKey({
            path: { orgID, projectID, integrationID: integration.id },
            client,
          }),
          integration,
        )
        await queryClient.invalidateQueries({
          queryKey: listIntegrationsQueryKey({ path: { orgID, projectID }, client }),
        })
      },
    },
  )
}

export function useUpdateIntegration(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      integrationID,
      ...body
    }: UpdateIntegrationRequest & { integrationID: string }) => {
      const { data } = await sdk.updateIntegration({
        path: { orgID, projectID, integrationID },
        body,
        client,
      })
      return data
    },
    onSuccess: async (integration, { integrationID }) => {
      const queryKey = getIntegrationQueryKey({
        path: { orgID, projectID, integrationID },
        client,
      })
      await queryClient.cancelQueries({ queryKey })
      queryClient.setQueryData<Integration>(queryKey, (previous) =>
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
          queryKey: listIntegrationsQueryKey({ path: { orgID, projectID }, client }),
        }),
      ])
    },
  })
}

export function useIntegrations(
  orgID: string,
  projectID: string,
  options?: PaginatedListOptions<ListIntegrationsData>,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListIntegrationsData>(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listIntegrationsInfiniteOptions({
        path: { orgID, projectID },
        query: list.query,
        client,
      }),
    ),
    enabled: list.enabled,
  })
}

export function useDeleteIntegration(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (integrationID: string) => {
      const { data } = await sdk.deleteIntegration({
        path: { orgID, projectID, integrationID },
        client,
      })
      return data
    },
    onSuccess: async (_, integrationID) => {
      const queryKey = getIntegrationQueryKey({
        path: { orgID, projectID, integrationID },
        client,
      })
      await queryClient.cancelQueries({ queryKey })
      queryClient.removeQueries({ queryKey })
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: listIntegrationsQueryKey({ path: { orgID, projectID }, client }),
        }),
        queryClient.invalidateQueries({
          queryKey: listAgentsQueryKey({ path: { orgID, projectID }, client }),
        }),
        queryClient.invalidateQueries({
          queryKey: listOrgAgentsQueryKey({ path: { orgID }, client }),
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

export function useCreateIntegrationOAuthSetup(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  return useMutation({
    mutationFn: async ({
      integrationID,
      ...body
    }: CreateIntegrationOAuthSetupRequest & { integrationID: string }) => {
      const { data } = await sdk.createIntegrationOAuthSetup({
        path: { orgID, projectID, integrationID },
        body,
        client,
      })
      return data
    },
  })
}

export function useCreateIntegrationSlackSetup(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  // eslint-disable-next-line react-doctor/query-mutation-missing-invalidation -- Only starts OAuth; the integration changes when OAuth completes.
  return useMutation({
    mutationFn: async ({
      integrationID,
      ...body
    }: CreateSlackSetupRequest & { integrationID: string }) => {
      const { data } = await sdk.createIntegrationSlackSetup({
        path: { orgID, projectID, integrationID },
        body,
        client,
      })
      return data
    },
  })
}

export function useCreateIntegrationGitHubSetup(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const cache = useQueryClient()
  return useMutation({
    mutationFn: async ({
      integrationID,
      ...body
    }: CreateGitHubSetupRequest & { integrationID: string }) => {
      const { data } = await sdk.createIntegrationGitHubSetup({
        path: { orgID, projectID, integrationID },
        body,
        client,
      })
      return data
    },
    onError: async (_, { integrationID }) => {
      await cache.invalidateQueries({
        queryKey: getIntegrationQueryKey({
          path: { orgID, projectID, integrationID },
          client,
        }),
      })
    },
  })
}

export function useInspectIntegrationGitHubInstallations(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  // eslint-disable-next-line react-doctor/query-mutation-missing-invalidation -- Reads GitHub; no Omnara state changes.
  return useMutation({
    mutationFn: async ({
      integrationID,
      ...body
    }: InspectGitHubInstallationsRequest & { integrationID: string }) => {
      const { data } = await sdk.inspectIntegrationGitHubInstallations({
        path: { orgID, projectID, integrationID },
        body,
        client,
      })
      return data
    },
  })
}

export function useConfigureIntegration(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const cache = useQueryClient()
  return useMutation({
    mutationFn: async ({
      integrationID,
      ...body
    }: ConfigureIntegrationRequest & { integrationID: string }) => {
      const { data } = await sdk.configureIntegration({
        path: { orgID, projectID, integrationID },
        body,
        client,
      })
      return data
    },
    onSuccess: async (integration) => {
      const queryKey = getIntegrationQueryKey({
        path: { orgID, projectID, integrationID: integration.id },
        client,
      })
      await cache.cancelQueries({ queryKey })
      cache.setQueryData(queryKey, integration)
      await cache.invalidateQueries({
        queryKey: listIntegrationsQueryKey({ path: { orgID, projectID }, client }),
      })
    },
    onError: async (_, { integrationID }) => {
      await cache.invalidateQueries({
        queryKey: getIntegrationQueryKey({
          path: { orgID, projectID, integrationID },
          client,
        }),
      })
    },
  })
}

export function useDisconnectIntegration(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const cache = useQueryClient()
  return useMutation({
    mutationFn: async (integrationID: string) => {
      const { data } = await sdk.disconnectIntegration({
        path: { orgID, projectID, integrationID },
        client,
      })
      return data
    },
    onSuccess: async (integration, integrationID) => {
      const queryKey = getIntegrationQueryKey({
        path: { orgID, projectID, integrationID },
        client,
      })
      await cache.cancelQueries({ queryKey })
      cache.setQueryData(queryKey, integration)
      await Promise.all([
        cache.invalidateQueries({
          queryKey: listIntegrationsQueryKey({ path: { orgID, projectID }, client }),
        }),
        cache.invalidateQueries({
          queryKey: listAgentsQueryKey({ path: { orgID, projectID }, client }),
        }),
      ])
    },
  })
}
