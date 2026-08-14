import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { useArrayPagination } from '@/hooks/use-array-pagination'

function PaginatedFreshObjects() {
  const items = Array.from({ length: 16 }, (_, index) => ({ id: String(index) }))
  const { rows, pagination } = useArrayPagination(items, (item) => item.id)

  if (pagination.page === 0) pagination.onNext()

  return <span>{rows[0]?.id}</span>
}

describe('useArrayPagination', () => {
  it('preserves the page when fresh objects retain the same ordered keys', () => {
    expect(renderToStaticMarkup(<PaginatedFreshObjects />)).toBe('<span>15</span>')
  })
})
