import { useState } from 'react'

import type { PaginationControls } from '@/hooks/use-paged-query'

const PAGE_SIZE = 15

type ItemKey = string | number

interface PaginationState {
  itemKeys: ItemKey[]
  pageIndex: number
}

function sameOrderedKeys(left: ItemKey[], right: ItemKey[]) {
  return left.length === right.length && left.every((key, index) => key === right[index])
}

export function useArrayPagination<TItem>(items: TItem[], getItemKey: (item: TItem) => ItemKey) {
  const itemKeys = items.map(getItemKey)
  const [state, setState] = useState<PaginationState>(() => ({ itemKeys, pageIndex: 0 }))
  const itemsChanged = !sameOrderedKeys(state.itemKeys, itemKeys)
  if (itemsChanged) {
    setState({ itemKeys, pageIndex: 0 })
  }
  const pageCount = Math.max(Math.ceil(items.length / PAGE_SIZE), 1)
  const page = itemsChanged ? 0 : Math.min(state.pageIndex, pageCount - 1)
  const rows = items.slice(page * PAGE_SIZE, (page + 1) * PAGE_SIZE)

  const pagination: PaginationControls = {
    page,
    canPrev: page > 0,
    canNext: page < pageCount - 1,
    onPrev: () => {
      setState((current) => ({ ...current, pageIndex: Math.max(page - 1, 0) }))
    },
    onNext: () => {
      setState((current) => ({ ...current, pageIndex: Math.min(page + 1, pageCount - 1) }))
    },
  }

  return { rows, pagination }
}
