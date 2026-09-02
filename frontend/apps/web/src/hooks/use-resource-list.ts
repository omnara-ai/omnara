import { CREATED_RESOURCE_LIST_SORTS, RESOURCE_LIST_SORTS } from '@omnara/react'
import { useEffect, useState } from 'react'

export interface SortOption<TSort extends string = string> {
  label: string
  value: TSort
}

const sortFieldLabels = new Map([
  ['name', 'Name'],
  ['created_at', 'Created'],
  ['updated_at', 'Modified'],
])

export function createSortOptions<TSort extends string>(
  values: readonly TSort[],
  fieldLabelOverrides: Readonly<Record<string, string>> = {},
): readonly SortOption<TSort>[] {
  return values.map((value) => {
    const descending = value.startsWith('-')
    const field = descending ? value.slice(1) : value
    const fieldLabel = fieldLabelOverrides[field] ?? sortFieldLabels.get(field) ?? field
    const direction =
      field === 'name' ? (descending ? 'Z–A' : 'A–Z') : descending ? 'Newest' : 'Oldest'
    return { value, label: `${fieldLabel}: ${direction}` }
  })
}

export const resourceSortOptions = createSortOptions(RESOURCE_LIST_SORTS)
export const createdResourceSortOptions = createSortOptions(CREATED_RESOURCE_LIST_SORTS)

export function useResourceList<TSort extends string>(defaultSort: TSort) {
  const { search, setSearch, name } = useTypeaheadSearch()
  const [sort, setSort] = useState<TSort>(defaultSort)

  const apiFilters: Record<string, unknown> = name ? { name } : {}

  return {
    search,
    setSearch,
    sort,
    setSort,
    apiFilters,
    isFiltering: name !== undefined,
    queryKey: JSON.stringify([apiFilters, sort]),
  }
}

export function nameGlob(value: string) {
  if (value.includes('*') || value.includes('?')) return value
  return `*${value}*`
}

/** Glob that matches exactly one name, for point lookups through list filters. */
export function exactNameGlob(value: string) {
  return value.replace(/[\\*?]/g, (wildcard) => `\\${wildcard}`)
}

export function useDebouncedValue(value: string, delayMs = 250) {
  const [debounced, setDebounced] = useState(value)

  useEffect(() => {
    const timeout = window.setTimeout(() => {
      setDebounced(value.trim())
    }, delayMs)
    return () => {
      window.clearTimeout(timeout)
    }
  }, [delayMs, value])

  return debounced
}

export function useTypeaheadSearch() {
  const [search, setSearch] = useState('')
  const debouncedSearch = useDebouncedValue(search)
  const name = debouncedSearch === '' ? undefined : nameGlob(debouncedSearch)

  return {
    search,
    setSearch,
    name,
    /** Ready-to-spread list filters: `{ name }` while searching, `{}` otherwise. */
    filters: name === undefined ? {} : { name },
  }
}

export function filterAndSortLocalItems<TItem>(
  items: TItem[],
  controls: Pick<ReturnType<typeof useResourceList<string>>, 'search' | 'sort'>,
  accessors: Record<string, (item: TItem) => string | boolean | null | undefined>,
) {
  const normalizedSearch = controls.search.trim().toLowerCase()
  const filtered = items.filter((item) => {
    const name = String(accessors.name?.(item) ?? '').toLowerCase()
    return normalizedSearch === '' || name.includes(normalizedSearch)
  })

  const descending = controls.sort.startsWith('-')
  const sortKey = descending ? controls.sort.slice(1) : controls.sort
  const accessor = accessors[sortKey]
  if (!accessor) return filtered
  return [...filtered].sort((left, right) => {
    const leftValue = accessor(left)
    const rightValue = accessor(right)
    const compared = String(leftValue ?? '').localeCompare(String(rightValue ?? ''), undefined, {
      numeric: true,
    })
    return descending ? -compared : compared
  })
}
