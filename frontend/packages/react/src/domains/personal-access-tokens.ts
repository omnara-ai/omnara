import { type CreatePersonalAccessTokenRequest, sdk } from '@omnara/sdk'
import { listPersonalAccessTokensInfiniteOptions } from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { DEFAULT_LIST_PAGE_SIZE } from './list-options'
import { cursorPagination } from './pagination'

export function usePersonalAccessTokens(
  pageSize = DEFAULT_LIST_PAGE_SIZE,
  options?: { refetchInterval?: number | false },
) {
  const client = useOmnaraClient()
  return useInfiniteQuery({
    ...listPersonalAccessTokensInfiniteOptions({ query: { limit: pageSize }, client }),
    ...cursorPagination,
    refetchInterval: options?.refetchInterval,
  })
}

function invalidatePersonalAccessTokens(queryClient: ReturnType<typeof useQueryClient>) {
  return queryClient.invalidateQueries({
    predicate: (query) => {
      const entry = query.queryKey[0] as { _id?: string } | undefined
      return entry?._id === 'listPersonalAccessTokens'
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
