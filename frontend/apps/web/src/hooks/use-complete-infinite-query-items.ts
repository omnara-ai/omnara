import { useEffect } from 'react'

import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

interface CompleteInfiniteQuery<TItem> {
  data?: { pages: { data: TItem[] }[] }
  hasNextPage: boolean
  isError: boolean
  isFetching: boolean
  isPending: boolean
  fetchNextPage: () => Promise<unknown>
}

/**
 * Resolve every page of a query before exposing its items as authoritative.
 * This is reserved for exclusion sets where partial data would make the UI
 * offer resources that are not actually eligible for an operation.
 */
export function useCompleteInfiniteQueryItems<TItem>(
  query: CompleteInfiniteQuery<TItem>,
  enabled: boolean,
) {
  const items = useInfiniteQueryItems(query)
  const { fetchNextPage, hasNextPage, isError, isFetching } = query

  useEffect(() => {
    if (!enabled || !hasNextPage || isFetching || isError) return
    void fetchNextPage()
  }, [enabled, fetchNextPage, hasNextPage, isError, isFetching])

  // React Query retains the last successful pages during a background refetch.
  // Keep those fully paginated items visible so dependent dropdowns stay
  // populated, but do not call them authoritative until the refresh settles.
  // Callers use isComplete to gate operations that rely on the exclusion set.
  const hasCompleteItems = enabled && !query.isPending && !hasNextPage && !isError
  const isPending = enabled && (query.isPending || isFetching || hasNextPage) && !isError
  const isComplete = hasCompleteItems && !isFetching

  return { items: hasCompleteItems ? items : [], isComplete, isPending }
}
