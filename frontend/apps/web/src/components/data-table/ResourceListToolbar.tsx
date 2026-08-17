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
    <div className="flex min-w-0 flex-1 items-start">
      <div
        role="group"
        aria-label="Search and sort resources"
        className="flex min-w-[18rem] max-w-2xl flex-1 items-stretch"
      >
        <Input
          type="search"
          value={search}
          onChange={(event) => {
            onSearchChange(event.target.value)
          }}
          placeholder={placeholder}
          aria-label={placeholder}
          className="relative flex-1 rounded-l-lg rounded-r-none focus-visible:z-10"
        />

        <Select
          value={sort}
          onValueChange={(value) => {
            onSortChange(value as TSort)
          }}
        >
          <SelectTrigger
            className="relative min-w-44 rounded-l-none rounded-r-lg border-l-0 focus-visible:z-10"
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
