import type { DateRange } from 'react-day-picker'

import { CalendarIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Calendar } from '@/components/ui/calendar'
import {
  DropdownMenu,
  DropdownMenuContent,
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
  lastDaysUsageRange,
  lastHoursUsageRange,
  type UsageDateRange,
  usageDateRange,
  usageDateRangeLabel,
  usageRangePresetDays,
  usageRangePresetHours,
} from '@/components/usage/usage-date-range'

const allTimePreset = 'all'
const customPreset = 'custom'

function hoursPreset(hours: number) {
  return `${hours}h`
}

function presetValue(range: UsageDateRange) {
  if (range.hours !== undefined) return hoursPreset(range.hours)
  if (range.days !== undefined) return String(range.days)
  return range.from ? customPreset : allTimePreset
}

export function UsageDateRangeMenu({
  value,
  onChange,
}: {
  value: UsageDateRange
  onChange: (value: UsageDateRange) => void
}) {
  const selectedRange: DateRange | undefined =
    value.days === undefined && value.from ? { from: value.from, to: value.to } : undefined

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="sm" className="h-9 px-3 text-xs">
          <CalendarIcon className="size-3.5" />
          {usageDateRangeLabel(value)}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        <DropdownMenuRadioGroup
          value={presetValue(value)}
          onValueChange={(preset) => {
            if (preset === allTimePreset) {
              onChange(allTimeUsageRange)
              return
            }
            const hours = usageRangePresetHours.find((option) => hoursPreset(option) === preset)
            if (hours !== undefined) {
              onChange(lastHoursUsageRange(hours))
              return
            }
            const days = usageRangePresetDays.find((option) => String(option) === preset)
            if (days !== undefined) onChange(lastDaysUsageRange(days))
          }}
        >
          {usageRangePresetHours.map((hours) => (
            <DropdownMenuRadioItem key={hoursPreset(hours)} value={hoursPreset(hours)}>
              Last {hours} hours
            </DropdownMenuRadioItem>
          ))}
          {usageRangePresetDays.map((days) => (
            <DropdownMenuRadioItem key={days} value={String(days)}>
              Last {days} days
            </DropdownMenuRadioItem>
          ))}
          <DropdownMenuRadioItem value={allTimePreset}>All time</DropdownMenuRadioItem>
        </DropdownMenuRadioGroup>
        <DropdownMenuSeparator />
        <DropdownMenuSub>
          <DropdownMenuSubTrigger>
            <span className="flex-1">Custom range</span>
            {selectedRange && (
              <span className="text-muted-foreground truncate text-xs">
                {usageDateRangeLabel(value)}
              </span>
            )}
          </DropdownMenuSubTrigger>
          <DropdownMenuSubContent className="p-0">
            <Calendar
              mode="range"
              numberOfMonths={2}
              defaultMonth={value.from ?? new Date()}
              selected={selectedRange}
              onSelect={(range) => {
                onChange(range?.from ? usageDateRange(range.from, range.to) : allTimeUsageRange)
              }}
            />
          </DropdownMenuSubContent>
        </DropdownMenuSub>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
