import type { DateRange } from 'react-day-picker'

import { FunnelIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Calendar } from '@/components/ui/calendar'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  allTimeUsageRange,
  type UsageDateRange,
  usageDateRange,
  usageDateRangeLabel,
} from '@/components/usage/usage-date-range'
import {
  usageProjectFilterLabel,
  type UsageProjectFilterValue,
} from '@/components/usage/usage-project-filter'
import { UsageProjectMenuItems } from '@/components/usage/UsageProjectFilter'
import type { SortOption } from '@/hooks/use-resource-list'

interface ToggleFilter {
  checked: boolean
  onChange: (checked: boolean) => void
}

function keepMenuOpen(event: Event) {
  event.preventDefault()
}

export const trailingCheckboxItemClass =
  'pl-2 pr-8 [&>span:first-child]:left-auto [&>span:first-child]:right-2'

export function FiltersMenu<TSort extends string>({
  label = 'Filters',
  dateRange,
  sort,
  subagents,
  archived,
  projects,
}: {
  label?: string
  dateRange?: { value: UsageDateRange; onChange: (value: UsageDateRange) => void }
  sort?: { value: TSort; options: readonly SortOption<TSort>[]; onChange: (sort: TSort) => void }
  subagents?: ToggleFilter
  archived?: ToggleFilter
  projects?: {
    orgId: string
    value: UsageProjectFilterValue
    onChange: (value: UsageProjectFilterValue) => void
  }
}) {
  const selectedRange: DateRange | undefined = dateRange?.value.from
    ? { from: dateRange.value.from, to: dateRange.value.to }
    : undefined
  const hasToggles = subagents !== undefined || archived !== undefined
  const hasSubmenus = dateRange !== undefined || projects !== undefined || sort !== undefined

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="sm" className="h-9 px-3 text-xs">
          <FunnelIcon className="size-3.5" />
          {label}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        {dateRange && (
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>
              <span className="flex-1">Date range</span>
              <span className="text-muted-foreground truncate text-xs">
                {usageDateRangeLabel(dateRange.value)}
              </span>
            </DropdownMenuSubTrigger>
            <DropdownMenuSubContent className="p-0">
              <Calendar
                mode="range"
                numberOfMonths={2}
                defaultMonth={dateRange.value.from ?? new Date()}
                selected={selectedRange}
                onSelect={(range) => {
                  dateRange.onChange(usageDateRange(range?.from, range?.to))
                }}
              />
              {dateRange.value.from && (
                <>
                  <DropdownMenuSeparator className="my-0" />
                  <div className="p-1">
                    <DropdownMenuItem
                      onSelect={() => {
                        dateRange.onChange(allTimeUsageRange)
                      }}
                    >
                      Clear date range
                    </DropdownMenuItem>
                  </div>
                </>
              )}
            </DropdownMenuSubContent>
          </DropdownMenuSub>
        )}
        {projects && (
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>
              <span className="flex-1">Projects</span>
              <span className="text-muted-foreground truncate text-xs">
                {usageProjectFilterLabel(projects.value)}
              </span>
            </DropdownMenuSubTrigger>
            <DropdownMenuSubContent className="max-h-80 w-56 overflow-y-auto">
              <UsageProjectMenuItems
                orgId={projects.orgId}
                value={projects.value}
                onChange={projects.onChange}
              />
            </DropdownMenuSubContent>
          </DropdownMenuSub>
        )}
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
