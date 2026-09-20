import {
  ApiError,
  type ConfigureProjectAppRequest,
  type ListProjectAppsData,
  type SaveProjectAppRequest,
  sdk,
} from '@omnara/sdk'
import {
  getProjectAppOptions,
  getProjectAppQueryKey,
  listAgentsQueryKey,
  listAppDefinitionsOptions,
  listCronTriggersQueryKey,
  listProjectAppsInfiniteOptions,
  listProjectAppsQueryKey,
} from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { type PaginatedListOptions, paginatedListOptions } from './list-options'
import { cursorPaginated } from './pagination'
import { useScopedMutation } from './scoped-mutation'

export function useProjectApp(orgID: string, projectID: string, appID: string) {
  const client = useOmnaraClient()
  return useQuery({
    ...getProjectAppOptions({ path: { orgID, projectID, appID }, client }),
    enabled: appID !== '',
  })
}

export function useCreateProjectApp(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createProjectApp,
    { orgID, projectID },
    {
      onSuccess: async (app) => {
        queryClient.setQueryData(
          getProjectAppQueryKey({ path: { orgID, projectID, appID: app.id }, client }),
          app,
        )
        await queryClient.invalidateQueries({
          queryKey: listProjectAppsQueryKey({ path: { orgID, projectID }, client }),
        })
      },
    },
  )
}

export function useUpdateProjectApp(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ appID, ...body }: SaveProjectAppRequest & { appID: string }) => {
      const { data } = await sdk.updateProjectApp({
        path: { orgID, projectID, appID },
        body,
        client,
      })
      return data
    },
    onSuccess: async (app) => {
      const queryKey = getProjectAppQueryKey({ path: { orgID, projectID, appID: app.id }, client })
      await queryClient.cancelQueries({ queryKey })
      queryClient.setQueryData(queryKey, app)
      await queryClient.invalidateQueries({
        queryKey: listProjectAppsQueryKey({ path: { orgID, projectID }, client }),
      })
    },
  })
}

export function useProjectApps(
  orgID: string,
  projectID: string,
  options?: PaginatedListOptions<ListProjectAppsData>,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListProjectAppsData>(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listProjectAppsInfiniteOptions({ path: { orgID, projectID }, query: list.query, client }),
    ),
    enabled: list.enabled,
  })
}

export function useDeleteProjectApp(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (appID: string) => {
      const { data } = await sdk.deleteProjectApp({ path: { orgID, projectID, appID }, client })
      return data
    },
    onSuccess: async (_, appID) => {
      const queryKey = getProjectAppQueryKey({ path: { orgID, projectID, appID }, client })
      await queryClient.cancelQueries({ queryKey })
      queryClient.removeQueries({ queryKey })
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: listProjectAppsQueryKey({ path: { orgID, projectID }, client }),
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

export function useAppDefinitions(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  return useQuery(listAppDefinitionsOptions({ path: { orgID, projectID }, client }))
}

export function useCreateProjectAppOAuthSetup(orgID: string, projectID: string, appID: string) {
  return useScopedMutation(sdk.createProjectAppOAuthSetup, { orgID, projectID, appID })
}

export function useCreateProjectAppSlackSetup(orgID: string, projectID: string, appID: string) {
  return useScopedMutation(sdk.createProjectAppSlackSetup, { orgID, projectID, appID })
}

export function useConfigureProjectApp(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const cache = useQueryClient()
  return useMutation({
    mutationFn: async ({ appID, ...body }: ConfigureProjectAppRequest & { appID: string }) => {
      const { data } = await sdk.configureProjectApp({
        path: { orgID, projectID, appID },
        body,
        client,
      })
      return data
    },
    onSuccess: async (app) => {
      const queryKey = getProjectAppQueryKey({ path: { orgID, projectID, appID: app.id }, client })
      await cache.cancelQueries({ queryKey })
      cache.setQueryData(queryKey, app)
      await cache.invalidateQueries({
        queryKey: listProjectAppsQueryKey({ path: { orgID, projectID }, client }),
      })
    },
    onError: async (_, { appID }) => {
      // A stale setup revision requires a fresh app before retrying credentials.
      await cache.invalidateQueries({
        queryKey: getProjectAppQueryKey({ path: { orgID, projectID, appID }, client }),
      })
    },
  })
}

export function useDisconnectProjectApp(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const cache = useQueryClient()
  return useMutation({
    mutationFn: async (appID: string) => {
      const { data } = await sdk.disconnectProjectApp({ path: { orgID, projectID, appID }, client })
      return data
    },
    onSuccess: async (app, appID) => {
      const queryKey = getProjectAppQueryKey({ path: { orgID, projectID, appID }, client })
      await cache.cancelQueries({ queryKey })
      cache.setQueryData(queryKey, app)
      await Promise.all([
        cache.invalidateQueries({
          queryKey: listProjectAppsQueryKey({ path: { orgID, projectID }, client }),
        }),
        cache.invalidateQueries({
          queryKey: listAgentsQueryKey({ path: { orgID, projectID }, client }),
        }),
      ])
    },
  })
}

export function useProjectAppOAuthCompletion(
  orgID: string,
  projectID: string,
  appID: string,
  flow?: { flow_id: string; expires_at: string; setup_revision: number },
) {
  const client = useOmnaraClient()
  return useQuery({
    ...getProjectAppOptions({ path: { orgID, projectID, appID }, client }),
    enabled: Boolean(flow),
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
