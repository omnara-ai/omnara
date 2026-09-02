import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { SortOption } from '@/hooks/use-resource-list'

interface ResourceListToolbarProps<TSort extends string> {
  search: string
  onSearchChange: (value: string) => void
  sort: TSort
  sortOptions: readonly SortOption<TSort>[]
  onSortChange: (sort: TSort) => void
  placeholder: string
}

export function ResourceListToolbar<TSort extends string>({
  search,
  onSearchChange,
  sort,
  sortOptions,
  onSortChange,
  placeholder,
}: ResourceListToolbarProps<TSort>) {
  return (
    <div className="flex w-full min-w-0 flex-1 items-start sm:w-auto">
      <div
        role="group"
        aria-label="Search and sort resources"
        className="flex w-full min-w-0 max-w-2xl flex-1 flex-col items-stretch gap-2 sm:min-w-[18rem] sm:flex-row sm:gap-0"
      >
        <Input
          type="search"
          value={search}
          onChange={(event) => {
            onSearchChange(event.target.value)
          }}
          placeholder={placeholder}
          aria-label={placeholder}
          className="relative w-full rounded-lg focus-visible:z-10 sm:flex-1 sm:rounded-l-lg sm:rounded-r-none"
        />

        <Select
          value={sort}
          onValueChange={(value) => {
            onSortChange(value as TSort)
          }}
        >
          <SelectTrigger
            className="relative w-full rounded-lg focus-visible:z-10 sm:w-auto sm:min-w-44 sm:rounded-l-none sm:rounded-r-lg sm:border-l-0"
            aria-label="Sort results"
          >
            <SelectValue placeholder="Sort by">
              {sortOptions.find((option) => option.value === sort)?.label ?? sort}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            {sortOptions.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    </div>
  )
}
