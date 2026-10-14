import { FunnelIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import type { SortOption } from '@/hooks/use-resource-list'

interface ToggleFilter {
  checked: boolean
  onChange: (checked: boolean) => void
}

function keepMenuOpen(event: Event) {
  event.preventDefault()
}

const trailingCheckboxItemClass =
  'pl-2 pr-8 [&>span:first-child]:left-auto [&>span:first-child]:right-2'

export function FiltersMenu<TSort extends string>({
  label = 'Filters',
  sort,
  subagents,
  archived,
}: {
  label?: string
  sort?: { value: TSort; options: readonly SortOption<TSort>[]; onChange: (sort: TSort) => void }
  subagents?: ToggleFilter
  archived?: ToggleFilter
}) {
  const hasToggles = subagents !== undefined || archived !== undefined
  const hasSubmenus = sort !== undefined

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="sm" className="h-9 px-3 text-xs">
          <FunnelIcon className="size-3.5" />
          {label}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        {sort && (
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>
              <span className="flex-1">Sort by</span>
              <span className="text-muted-foreground truncate text-xs">
                {sort.options.find((option) => option.value === sort.value)?.label}
              </span>
            </DropdownMenuSubTrigger>
            <DropdownMenuSubContent className="w-48">
              <DropdownMenuRadioGroup
                value={sort.value}
                onValueChange={(value) => {
                  const option = sort.options.find((candidate) => candidate.value === value)
                  if (option) sort.onChange(option.value)
                }}
              >
                {sort.options.map((option) => (
                  <DropdownMenuRadioItem key={option.value} value={option.value}>
                    {option.label}
                  </DropdownMenuRadioItem>
                ))}
              </DropdownMenuRadioGroup>
            </DropdownMenuSubContent>
          </DropdownMenuSub>
        )}
        {hasSubmenus && hasToggles && <DropdownMenuSeparator />}
        {subagents && (
          <DropdownMenuCheckboxItem
            className={trailingCheckboxItemClass}
            checked={subagents.checked}
            onSelect={keepMenuOpen}
            onCheckedChange={subagents.onChange}
          >
            Subagents
          </DropdownMenuCheckboxItem>
        )}
        {archived && (
          <DropdownMenuCheckboxItem
            className={trailingCheckboxItemClass}
            checked={archived.checked}
            onSelect={keepMenuOpen}
            onCheckedChange={archived.onChange}
          >
            Archived
          </DropdownMenuCheckboxItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
