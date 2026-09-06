interface CursorPage {
  next_cursor: string | null
}

interface CursorPaginatedOptions<Key extends { path?: unknown }> {
  queryKey: readonly [Key, ...unknown[]]
}

/**
 * Cursor pagination glue for generated infinite query options: the generated
 * options intentionally leave page-param wiring to the caller. The generated
 * queryFn merges an object page param into the request and treats any other
 * page param as the cursor value, so the first page reuses the key's path and
 * requests without a cursor.
 */
export function cursorPaginated<
  Key extends { path?: unknown },
  Options extends CursorPaginatedOptions<Key>,
>(options: Options) {
  return {
    ...options,
    initialPageParam: { path: options.queryKey[0].path },
    getNextPageParam: (lastPage: CursorPage) => lastPage.next_cursor ?? undefined,
  }
}

export { DEFAULT_LIST_PAGE_SIZE } from './list-options'
