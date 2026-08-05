import { useMemo } from 'react'

export function useInfiniteQueryItems<TItem>(query: { data?: { pages: { data: TItem[] }[] } }) {
  return useMemo(() => query.data?.pages.flatMap((page) => page.data) ?? [], [query.data])
}
