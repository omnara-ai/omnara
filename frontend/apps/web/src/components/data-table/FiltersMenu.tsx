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

interface ToggleFilter {
  checked: boolean
  onChange: (checked: boolean) => void
}

interface ChoiceFilter<TValue extends string> {
  value: TValue
  options: readonly { value: TValue; label: string }[]
  onChange: (value: TValue) => void
}

function keepMenuOpen(event: Event) {
  event.preventDefault()
}

function ChoiceSubmenu<TValue extends string>({
  label,
  filter,
}: {
  label: string
  filter: ChoiceFilter<TValue>
}) {
  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger>
        <span className="flex-1">{label}</span>
        <span className="text-muted-foreground truncate text-xs">
          {filter.options.find((option) => option.value === filter.value)?.label}
        </span>
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent className="w-48">
        <DropdownMenuRadioGroup
          value={filter.value}
          onValueChange={(value) => {
            const option = filter.options.find((candidate) => candidate.value === value)
            if (option) filter.onChange(option.value)
          }}
        >
          {filter.options.map((option) => (
            <DropdownMenuRadioItem key={option.value} value={option.value}>
              {option.label}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  )
}

const trailingCheckboxItemClass =
  'pl-2 pr-8 [&>span:first-child]:left-auto [&>span:first-child]:right-2'

export function FiltersMenu<TSort extends string, TActivity extends string = string>({
  label = 'Filters',
  sort,
  activity,
  subagents,
  archived,
}: {
  label?: string
  sort?: ChoiceFilter<TSort>
  activity?: ChoiceFilter<TActivity>
  subagents?: ToggleFilter
  archived?: ToggleFilter
}) {
  const hasToggles = subagents !== undefined || archived !== undefined
  const hasSubmenus = sort !== undefined || activity !== undefined

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="sm" className="h-9 px-3 text-xs">
          <FunnelIcon className="size-3.5" />
          {label}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        {sort && <ChoiceSubmenu label="Sort by" filter={sort} />}
        {activity && <ChoiceSubmenu label="Status" filter={activity} />}
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
