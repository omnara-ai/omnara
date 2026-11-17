import type { BudgetInterval } from '@omnara/sdk'

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { budgetIntervalLabel, budgetIntervals } from '@/components/usage/budget-form'

export function BudgetIntervalSelect({
  id,
  value,
  options = budgetIntervals,
  ariaLabel,
  disabled,
  onChange,
}: {
  id: string
  value: BudgetInterval
  options?: typeof budgetIntervals
  ariaLabel?: string
  disabled?: boolean
  onChange: (value: BudgetInterval) => void
}) {
  return (
    <Select
      value={value}
      disabled={disabled}
      onValueChange={(next) => {
        const option = options.find((item) => item.value === next)
        if (option) onChange(option.value)
      }}
    >
      <SelectTrigger id={id} aria-label={ariaLabel} className="w-full">
        <SelectValue>{budgetIntervalLabel(value)}</SelectValue>
      </SelectTrigger>
      <SelectContent>
        {options.map((option) => (
          <SelectItem key={option.value} value={option.value}>
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
