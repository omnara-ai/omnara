import type { BudgetInterval } from '@omnara/sdk'
import { zBudgetAmount } from '@omnara/sdk/zod'

export const budgetIntervals: { value: BudgetInterval; label: string }[] = [
  { value: 'day', label: 'Daily' },
  { value: 'week', label: 'Weekly' },
  { value: 'month', label: 'Monthly' },
  { value: 'lifetime', label: 'Lifetime' },
]

export function budgetIntervalLabel(interval: BudgetInterval) {
  return budgetIntervals.find((option) => option.value === interval)?.label ?? interval
}

export function budgetAmountValid(amount: string) {
  return zBudgetAmount.safeParse(amount).success && Number(amount) > 0
}

export interface BudgetDraft {
  interval: BudgetInterval
  amount: string
}
