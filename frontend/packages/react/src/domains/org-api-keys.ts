import { type CreateOrgApiKeyRequest, sdk, type UpdateOrgApiKeyRequest } from '@omnara/sdk'
import {
  listOrgApiKeyProjectAccessOptions,
  listOrgApiKeyProjectAccessQueryKey,
  listOrgApiKeysInfiniteOptions,
} from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { DEFAULT_LIST_PAGE_SIZE } from './list-options'
import { cursorPaginated } from './pagination'
import { generatedQueryKey } from './query-keys'

export function useOrgApiKeys(orgID: string, pageSize = DEFAULT_LIST_PAGE_SIZE) {
  const client = useOmnaraClient()
  return useInfiniteQuery({
    ...cursorPaginated(
      listOrgApiKeysInfiniteOptions({ path: { orgID }, query: { limit: pageSize }, client }),
    ),
  })
}

function invalidateOrgApiKeys(queryClient: ReturnType<typeof useQueryClient>) {
  return queryClient.invalidateQueries({
    predicate: (query) => {
      return generatedQueryKey(query)?._id === 'listOrgApiKeys'
    },
  })
}

export function useCreateOrgApiKey(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (body: CreateOrgApiKeyRequest) => {
      const { data } = await sdk.createOrgApiKey({ path: { orgID }, body, client })
      return data
    },
    onSuccess: async () => {
      await invalidateOrgApiKeys(queryClient)
    },
  })
}

export function useUpdateOrgApiKey(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ keyID, ...body }: UpdateOrgApiKeyRequest & { keyID: string }) => {
      const { data } = await sdk.updateOrgApiKey({ path: { orgID, keyID }, body, client })
      return data
    },
    onSuccess: async () => {
      await invalidateOrgApiKeys(queryClient)
    },
  })
}

export function useRevokeOrgApiKey(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (keyID: string) => {
      const { data } = await sdk.revokeOrgApiKey({ path: { orgID, keyID }, client })
      return data
    },
    onSuccess: async () => {
      await invalidateOrgApiKeys(queryClient)
    },
  })
}

export function useOrgApiKeyProjectAccess(
  orgID: string,
  keyID: string,
  options?: { enabled?: boolean },
) {
  const client = useOmnaraClient()
  return useQuery({
    ...listOrgApiKeyProjectAccessOptions({ path: { orgID, keyID }, client }),
    enabled: (options?.enabled ?? true) && keyID !== '',
  })
}

export function useSetOrgApiKeyProjectRole(orgID: string, keyID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ projectID, role }: { projectID: string; role: string }) => {
      const { data } = await sdk.setOrgApiKeyProjectRole({
        path: { orgID, keyID, projectID },
        body: { role },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listOrgApiKeyProjectAccessQueryKey({ path: { orgID, keyID }, client }),
      })
    },
  })
}

export function useRemoveOrgApiKeyProjectRole(orgID: string, keyID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (projectID: string) => {
      const { data } = await sdk.removeOrgApiKeyProjectRole({
        path: { orgID, keyID, projectID },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listOrgApiKeyProjectAccessQueryKey({ path: { orgID, keyID }, client }),
      })
    },
  })
}
