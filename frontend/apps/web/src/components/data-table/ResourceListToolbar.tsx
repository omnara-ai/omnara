import type { ComponentProps } from 'react'

import { FiltersMenu } from '@/components/data-table/FiltersMenu'
import { Input } from '@/components/ui/input'
import type { SortOption } from '@/hooks/use-resource-list'

type FilterProps<TSort extends string> = Omit<
  ComponentProps<typeof FiltersMenu<TSort>>,
  'label' | 'sort'
>

export function ResourceListToolbar<TSort extends string>({
  search,
  onSearchChange,
  placeholder,
  showSearch,
  sort,
  filters,
}: {
  search: string
  onSearchChange: (value: string) => void
  placeholder: string
  showSearch: boolean
  sort: { value: TSort; options: readonly SortOption<TSort>[]; onChange: (sort: TSort) => void }
  filters?: FilterProps<TSort>
}) {
  const hasFilters = filters !== undefined && Object.values(filters).some(Boolean)
  if (!showSearch && !hasFilters) return null
  const label = hasFilters ? (showSearch ? 'Filters & sort' : 'Filters') : 'Sort'
  return (
    <div className="flex min-w-0 flex-1 items-center justify-end gap-2">
      {showSearch && (
        <Input
          type="search"
          value={search}
          onChange={(event) => {
            onSearchChange(event.target.value)
          }}
          placeholder={placeholder}
          aria-label={placeholder}
          className="h-9 min-w-0 flex-1 text-sm"
        />
      )}
      <FiltersMenu label={label} sort={showSearch ? sort : undefined} {...filters} />
    </div>
  )
}
