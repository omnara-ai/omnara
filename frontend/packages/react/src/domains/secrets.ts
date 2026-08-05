import {
  type ListProjectAvailableSecretsData,
  type ListSecretGrantsData,
  type ListSecretsData,
  sdk,
  type SecretOwnerInput,
  type UpdateSecretRequest,
} from '@omnara/sdk'
import {
  listProjectAvailableSecretsInfiniteOptions,
  listSecretGrantsInfiniteOptions,
  listSecretsInfiniteOptions,
} from '@omnara/sdk/tanstack'
import {
  type QueryClient,
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import {
  type ListFilters,
  type ListSort,
  type PaginatedListOptions,
  paginatedListOptions,
} from './list-options'
import { cursorPagination } from './pagination'
import { useScopedMutation } from './scoped-mutation'

/**
 * Secret ownership scope as surfaced to UI code. Scope is data on the flat
 * secrets API: it selects the `owner` of a created secret and the owner
 * filter of secret lists, not which endpoint is called.
 */
export type SecretOwnerScope = SecretOwnerInput
/** Owner scope travels through the dedicated `owner` argument, not filters. */
export type SecretListFilters = Omit<
  ListFilters<ListSecretsData>,
  'owner_kind' | 'owner_project_id'
>
export type SecretListSort = ListSort<ListSecretsData>
export type SecretListOptions = Omit<PaginatedListOptions<ListSecretsData>, 'filters'> & {
  filters?: SecretListFilters
}

function ownerFilterQuery(owner: SecretOwnerScope | undefined) {
  if (!owner) return undefined
  return {
    owner_kind: owner.kind,
    ...(owner.kind === 'project' ? { owner_project_id: owner.project_id } : undefined),
  }
}
export type ProjectAvailableSecretListFilters = ListFilters<ListProjectAvailableSecretsData>
export type ProjectAvailableSecretListSort = ListSort<ListProjectAvailableSecretsData>
export type ProjectAvailableSecretListOptions =
  PaginatedListOptions<ListProjectAvailableSecretsData>
export type SecretGrantListFilters = ListFilters<ListSecretGrantsData>
export type SecretGrantListSort = ListSort<ListSecretGrantsData>
export type SecretGrantListOptions = PaginatedListOptions<ListSecretGrantsData>

/**
 * Secrets visible through ownership authority, optionally filtered to one
 * owner scope (org, user, or a specific project).
 */
export function useSecrets(orgID: string, owner?: SecretOwnerScope, options?: SecretListOptions) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListSecretsData>(options)
  return useInfiniteQuery({
    ...listSecretsInfiniteOptions({
      path: { orgID },
      query: { ...ownerFilterQuery(owner), ...list.query },
      client,
    }),
    ...cursorPagination,
    enabled: list.enabled,
  })
}

/**
 * Secrets available to a project — directly owned plus granted — with their
 * availability source.
 */
export function useProjectAvailableSecrets(
  orgID: string,
  projectID: string,
  options?: ProjectAvailableSecretListOptions,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListProjectAvailableSecretsData>(options)
  return useInfiniteQuery({
    ...listProjectAvailableSecretsInfiniteOptions({
      path: { orgID, projectID },
      query: list.query,
      client,
    }),
    ...cursorPagination,
    enabled: list.enabled,
  })
}

export function useSecretGrants(orgID: string, secretID: string, options?: SecretGrantListOptions) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListSecretGrantsData>(options)
  return useInfiniteQuery({
    ...listSecretGrantsInfiniteOptions({
      path: { orgID, secretID },
      query: list.query,
      client,
    }),
    ...cursorPagination,
    enabled: list.enabled,
  })
}

const SECRET_LIST_OPERATIONS = new Set([
  'listSecrets',
  'listProjectAvailableSecrets',
  'listSecretGrants',
])

/**
 * Invalidate every secret list in the org: ownership lists across all owner
 * filters plus every project's availability view. A secret created, deleted,
 * or (un)granted can surface in any of them.
 */
function invalidateSecretLists(queryClient: QueryClient, orgID: string) {
  return queryClient.invalidateQueries({
    predicate: (query) => {
      const entry = query.queryKey[0] as { _id?: string; path?: { orgID?: string } } | undefined
      return (
        entry?._id !== undefined &&
        SECRET_LIST_OPERATIONS.has(entry._id) &&
        entry.path?.orgID === orgID
      )
    },
  })
}

export function useCreateSecret(orgID: string) {
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createSecret,
    { orgID },
    {
      onSuccess: async () => {
        await invalidateSecretLists(queryClient, orgID)
      },
    },
  )
}

export function useDeleteSecret(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (secretID: string) => {
      const { data } = await sdk.deleteSecret({ path: { orgID, secretID }, client })
      return data
    },
    onSuccess: async () => {
      await invalidateSecretLists(queryClient, orgID)
    },
  })
}

export function useDeleteSecretGrant(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ secretID, grantID }: { secretID: string; grantID: string }) => {
      const { data } = await sdk.deleteSecretGrant({
        path: { orgID, secretID, grantID },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await invalidateSecretLists(queryClient, orgID)
    },
  })
}

export function useUpdateSecret(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ secretID, ...body }: UpdateSecretRequest & { secretID: string }) => {
      const { data } = await sdk.updateSecret({ path: { orgID, secretID }, body, client })
      return data
    },
    onSuccess: async () => {
      await invalidateSecretLists(queryClient, orgID)
    },
  })
}

export function useGrantSecretToProject(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ secretID, projectID }: { secretID: string; projectID: string }) => {
      const { data } = await sdk.createSecretGrant({
        path: { orgID, secretID },
        body: { target_project_id: projectID },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await invalidateSecretLists(queryClient, orgID)
    },
  })
}

export function useStartSecretMcpOAuth(orgID: string) {
  return useScopedMutation(sdk.startSecretMcpoAuth, { orgID })
}
