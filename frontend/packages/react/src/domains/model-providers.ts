import {
  type ConfiguredModel,
  type CreateConfiguredModelRequest,
  type CreateModelProviderConfigRequest,
  type ListModelProviderConfigsData,
  type ModelProviderConfig,
  type OmnaraClient,
  sdk,
  type UpdateConfiguredModelRequest,
  type UpdateModelProviderConfigRequest,
} from '@omnara/sdk'
import {
  getModelProviderConfigOptions,
  getModelProviderConfigQueryKey,
  listConfiguredModelsInfiniteOptions,
  listModelProviderConfigsInfiniteOptions,
  listModelProviderConfigsQueryKey,
} from '@omnara/sdk/tanstack'
import {
  type QueryClient,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import {
  DEFAULT_LIST_PAGE_SIZE,
  type ListFilters,
  type ListSort,
  type PaginatedListOptions,
  paginatedListOptions,
} from './list-options'
import { cursorPagination } from './pagination'

export type ModelProviderListFilters = ListFilters<ListModelProviderConfigsData>
export type ModelProviderListSort = ListSort<ListModelProviderConfigsData>
export type ModelProviderListOptions = PaginatedListOptions<ListModelProviderConfigsData>

export function useModelProviders(orgID: string, options?: ModelProviderListOptions) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListModelProviderConfigsData>(options)
  return useInfiniteQuery({
    ...listModelProviderConfigsInfiniteOptions({
      path: { orgID },
      query: list.query,
      client,
    }),
    ...cursorPagination,
    enabled: list.enabled,
  })
}

/**
 * Single provider config plus its model discovery result. The route probes the
 * provider's /models endpoint on every fetch, so keep it disabled until the
 * discovery data is actually needed.
 */
export function useModelProvider(
  orgID: string,
  modelProviderConfigID: string,
  options?: { enabled?: boolean },
) {
  const client = useOmnaraClient()
  return useQuery({
    ...getModelProviderConfigOptions({ path: { orgID, modelProviderConfigID }, client }),
    enabled: (options?.enabled ?? true) && modelProviderConfigID !== '',
  })
}

export function useConfiguredModels(
  orgID: string,
  modelProviderConfigID: string,
  options?: { enabled?: boolean },
) {
  const client = useOmnaraClient()
  return useInfiniteQuery({
    ...listConfiguredModelsInfiniteOptions({
      path: { orgID, modelProviderConfigID },
      query: { limit: DEFAULT_LIST_PAGE_SIZE },
      client,
    }),
    ...cursorPagination,
    enabled: (options?.enabled ?? true) && modelProviderConfigID !== '',
  })
}

export interface ModelOption {
  model: ConfiguredModel
  provider: ModelProviderConfig
}

async function fetchConfiguredModelsForProvider(
  client: OmnaraClient,
  orgID: string,
  provider: ModelProviderConfig,
) {
  const models: ModelOption[] = []
  let cursor: string | undefined

  do {
    const { data: page } = await sdk.listConfiguredModels({
      client,
      path: { orgID, modelProviderConfigID: provider.id },
      query: { cursor },
    })
    models.push(...page.data.map((model) => ({ model, provider })))
    cursor = page.next_cursor ?? undefined
  } while (cursor)

  return models
}

/**
 * Every configured model across the given provider configs, sorted by
 * provider then model name. Keyed like a generated listConfiguredModels key
 * (plus a local suffix) so invalidateConfiguredModels reaches it; the
 * provider IDs in the key refetch the aggregate when the provider list
 * changes.
 */
export function useConfiguredModelOptions(orgID: string, providers: ModelProviderConfig[]) {
  const client = useOmnaraClient()
  return useQuery({
    queryKey: [
      { _id: 'listConfiguredModels', baseUrl: client.getConfig().baseUrl, path: { orgID } },
      'options',
      providers.map((provider) => provider.id),
    ],
    queryFn: async () => {
      const models = (
        await Promise.all(
          providers.map((provider) => fetchConfiguredModelsForProvider(client, orgID, provider)),
        )
      ).flat()

      return models.sort((left, right) => {
        const providerName = left.provider.name.localeCompare(right.provider.name)
        return providerName || left.model.name.localeCompare(right.model.name)
      })
    },
    enabled: providers.length > 0,
  })
}

/**
 * Invalidate configured-model lists across every provider config in the org,
 * including aggregation caches shaped like generated listConfiguredModels
 * keys (useConfiguredModelOptions above and app-level equivalents).
 */
function invalidateConfiguredModels(queryClient: QueryClient, orgID: string) {
  return queryClient.invalidateQueries({
    predicate: (query) => {
      const entry = query.queryKey[0] as { _id?: string; path?: { orgID?: string } } | undefined
      return entry?._id === 'listConfiguredModels' && entry.path?.orgID === orgID
    },
  })
}

export function useCreateModelProvider(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (body: CreateModelProviderConfigRequest) => {
      const { data, response } = await sdk.createModelProviderConfig({
        path: { orgID },
        body,
        client,
      })
      // Preserve the route's idempotency signal so callers only roll back resources
      // created by their own request, rather than a same-intent config returned with 200.
      return { ...data, created: response.status === 201 }
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listModelProviderConfigsQueryKey({ path: { orgID }, client }),
      })
    },
  })
}

/**
 * The target provider config is picked inside the create dialog, so it stays
 * a mutation variable rather than hook scope.
 */
export function useCreateConfiguredModel(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      modelProviderConfigID,
      ...body
    }: CreateConfiguredModelRequest & { modelProviderConfigID: string }) => {
      const { data } = await sdk.createConfiguredModel({
        client,
        path: { orgID, modelProviderConfigID },
        body,
      })
      return data
    },
    onSuccess: async () => {
      await invalidateConfiguredModels(queryClient, orgID)
    },
  })
}

export function useUpdateModelProvider(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      modelProviderConfigID,
      ...body
    }: UpdateModelProviderConfigRequest & { modelProviderConfigID: string }) => {
      const { data } = await sdk.updateModelProviderConfig({
        path: { orgID, modelProviderConfigID },
        body,
        client,
      })
      return data
    },
    onSuccess: async (_data, { modelProviderConfigID }) => {
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: listModelProviderConfigsQueryKey({ path: { orgID }, client }),
        }),
        queryClient.invalidateQueries({
          queryKey: getModelProviderConfigQueryKey({
            path: { orgID, modelProviderConfigID },
            client,
          }),
        }),
      ])
    },
  })
}

export function useDeleteModelProvider(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (modelProviderConfigID: string) => {
      const { data } = await sdk.deleteModelProviderConfig({
        path: { orgID, modelProviderConfigID },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listModelProviderConfigsQueryKey({ path: { orgID }, client }),
      })
      await invalidateConfiguredModels(queryClient, orgID)
    },
  })
}

export function useUpdateConfiguredModel(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      modelProviderConfigID,
      configuredModelID,
      ...body
    }: UpdateConfiguredModelRequest & {
      modelProviderConfigID: string
      configuredModelID: string
    }) => {
      const { data } = await sdk.updateConfiguredModel({
        path: { orgID, modelProviderConfigID, configuredModelID },
        body,
        client,
      })
      return data
    },
    onSuccess: async () => {
      await invalidateConfiguredModels(queryClient, orgID)
    },
  })
}

export function useDeleteConfiguredModel(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      modelProviderConfigID,
      configuredModelID,
    }: {
      modelProviderConfigID: string
      configuredModelID: string
    }) => {
      const { data } = await sdk.deleteConfiguredModel({
        path: { orgID, modelProviderConfigID, configuredModelID },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await invalidateConfiguredModels(queryClient, orgID)
    },
  })
}
