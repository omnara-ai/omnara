interface CursorPage {
  next_cursor: string | null
}

/**
 * Cursor pagination glue for generated infinite query options: the generated
 * options intentionally leave page-param wiring to the caller. The generated
 * queryFn treats a non-object page param as the cursor value and drops an
 * undefined cursor from the query string, so the first page is requested
 * without a cursor. The cast is needed because the generated page-param
 * union has no undefined arm.
 */
export const cursorPagination = {
  initialPageParam: undefined as unknown as string,
  getNextPageParam: (lastPage: CursorPage) => lastPage.next_cursor ?? undefined,
}

export { DEFAULT_LIST_PAGE_SIZE } from './list-options'
