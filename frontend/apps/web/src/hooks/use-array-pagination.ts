import { useEffect, useRef, useState } from 'react'

import type { PaginationControls } from '@/hooks/use-paged-query'

const PAGE_SIZE = 15

/**
 * Page through an in-memory array with the same controls shape as
 * usePagedQuery, for lists derived client-side from a drained query.
 */
export function useArrayPagination<TItem>(items: TItem[]) {
  const [pageIndex, setPageIndex] = useState(0)
  const previousItems = useRef(items)
  const itemsChanged =
    previousItems.current.length !== items.length ||
    previousItems.current.some((item, index) => item !== items[index])
  useEffect(() => {
    if (!itemsChanged) return
    previousItems.current = items
    setPageIndex(0)
  }, [items, itemsChanged])
  const pageCount = Math.max(Math.ceil(items.length / PAGE_SIZE), 1)
  const page = Math.min(pageIndex, pageCount - 1)
  const rows = items.slice(page * PAGE_SIZE, (page + 1) * PAGE_SIZE)

  const pagination: PaginationControls = {
    page,
    canPrev: page > 0,
    canNext: page < pageCount - 1,
    onPrev: () => {
      setPageIndex(Math.max(page - 1, 0))
    },
    onNext: () => {
      setPageIndex(Math.min(page + 1, pageCount - 1))
    },
  }

  return { rows, pagination }
}
