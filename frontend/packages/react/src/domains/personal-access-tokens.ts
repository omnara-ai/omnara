import { type CreatePersonalAccessTokenRequest, sdk } from '@omnara/sdk'
import { listPersonalAccessTokensInfiniteOptions } from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { DEFAULT_LIST_PAGE_SIZE } from './list-options'
import { cursorPaginated } from './pagination'
import { generatedQueryKey } from './query-keys'

export function usePersonalAccessTokens(
  pageSize = DEFAULT_LIST_PAGE_SIZE,
  options?: { refetchInterval?: number | false },
) {
  const client = useOmnaraClient()
  return useInfiniteQuery({
    ...cursorPaginated(
      listPersonalAccessTokensInfiniteOptions({ query: { limit: pageSize }, client }),
    ),
    refetchInterval: options?.refetchInterval,
  })
}

function invalidatePersonalAccessTokens(queryClient: ReturnType<typeof useQueryClient>) {
  return queryClient.invalidateQueries({
    predicate: (query) => {
      return generatedQueryKey(query)?._id === 'listPersonalAccessTokens'
    },
  })
}

export function useCreatePersonalAccessToken() {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (body: CreatePersonalAccessTokenRequest) => {
      const { data } = await sdk.createPersonalAccessToken({ body, client })
      return data
    },
    onSuccess: async () => {
      await invalidatePersonalAccessTokens(queryClient)
    },
  })
}

export function useRevokePersonalAccessToken() {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (tokenID: string) => {
      const { data } = await sdk.revokePersonalAccessToken({ path: { tokenID }, client })
      return data
    },
    onSuccess: async () => {
      await invalidatePersonalAccessTokens(queryClient)
    },
  })
}
