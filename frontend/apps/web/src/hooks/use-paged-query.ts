import { useState } from 'react'

export interface PaginationControls {
  page: number
  canPrev: boolean
  canNext: boolean
  onPrev: () => void
  onNext: () => void
}

interface CursorQuery<TItem> {
  data?: { pages: { data: TItem[] }[] }
  hasNextPage: boolean
  isFetchingNextPage: boolean
  fetchNextPage: () => Promise<unknown>
}

/**
 * Walk a cursor-paginated infinite query one API page at a time. Next fetches
 * the following page on demand; already-fetched pages are revisited from
 * cache. There is no total — the API doesn't report one.
 */
export function usePagedQuery<TItem>(query: CursorQuery<TItem>, resetKey?: unknown) {
  const [pageIndex, setPageIndex] = useState(0)
  const [previousResetKey, setPreviousResetKey] = useState(resetKey)
  if (!Object.is(previousResetKey, resetKey)) {
    setPreviousResetKey(resetKey)
    setPageIndex(0)
  }
  const pages = query.data?.pages ?? []
  const page = Math.min(pageIndex, Math.max(pages.length - 1, 0))
  const rows = pages[page]?.data ?? []

  const pagination: PaginationControls = {
    page,
    canPrev: page > 0,
    canNext: page < pages.length - 1 || query.hasNextPage,
    onPrev: () => {
      setPageIndex(Math.max(page - 1, 0))
    },
    onNext: () => {
      if (page < pages.length - 1) {
        setPageIndex(page + 1)
        return
      }
      if (!query.hasNextPage || query.isFetchingNextPage) return
      void query.fetchNextPage().then(() => {
        setPageIndex(page + 1)
      })
    },
  }

  return {
    rows,
    /** Every item fetched so far, across all loaded pages. */
    loaded: pages.flatMap((loadedPage) => loadedPage.data),
    pagination,
  }
}
