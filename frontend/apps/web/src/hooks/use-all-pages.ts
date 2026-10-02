import { useEffect } from 'react'

import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

interface CursorQuery<TItem, TFetchResult> {
  data?: { pages: { data: TItem[] }[] }
  isPending: boolean
  isError: boolean
  hasNextPage: boolean
  isFetchingNextPage: boolean
  fetchNextPage: () => Promise<TFetchResult>
}

/**
 * Walk every page of a cursor-paginated query, for views that need the whole
 * set at once (client-side ordering, grouping, or a complete count). Stays
 * pending until the last page arrives so callers never render a partial set.
 */
export function useAllPages<TItem, TFetchResult>(query: CursorQuery<TItem, TFetchResult>) {
  const { fetchNextPage, hasNextPage, isFetchingNextPage, isError } = query
  useEffect(() => {
    if (!hasNextPage || isFetchingNextPage || isError) return
    void fetchNextPage()
  }, [fetchNextPage, hasNextPage, isFetchingNextPage, isError])
  return {
    items: useInfiniteQueryItems(query),
    isPending: query.isPending || (hasNextPage && !isError),
    isError,
  }
}
