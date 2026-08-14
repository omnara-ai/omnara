export function useInfiniteQueryItems<TItem>(query: { data?: { pages: { data: TItem[] }[] } }) {
  return query.data?.pages.flatMap((page) => page.data) ?? []
}
