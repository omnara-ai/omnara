import { useBudgets, useCreateBudget, useDeleteBudget, useUpdateBudget } from '@omnara/react'
import type { Budget, BudgetInterval } from '@omnara/sdk'
import { zBudgetAmount } from '@omnara/sdk/zod'
import { type SyntheticEvent, useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { spendReachesBudget } from '@/components/usage/budget-spend'
import { formatUsd } from '@/lib/format'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError, submitting } from '@/lib/submit-status'
import { cn } from '@/lib/utils'

const intervals: { value: BudgetInterval; label: string }[] = [
  { value: 'day', label: 'Daily' },
  { value: 'week', label: 'Weekly' },
  { value: 'month', label: 'Monthly' },
  { value: 'lifetime', label: 'Lifetime' },
]

type Scope = 'organization' | 'project'

export function BudgetCard({ orgId, projectId }: { orgId: string; projectId?: string }) {
  const budgets = useBudgets(orgId, projectId)
  const [editing, setEditing] = useState<Budget | 'new' | null>(null)
  const scope: Scope = projectId ? 'project' : 'organization'
  if (budgets.isError) {
    return (
      <p className="text-destructive rounded-xl border px-4 py-3 text-sm" role="alert">
        Could not load budgets.
      </p>
    )
  }
  if (!budgets.data) {
    return <Skeleton className="h-28 rounded-xl" />
  }
  const byInterval = new Map(budgets.data.data.map((budget) => [budget.interval, budget]))
  const available = intervals.filter(({ value }) => !byInterval.has(value))
  return (
    <section className="bg-muted/50 rounded-xl border">
      <div className="bg-card -mx-px -mt-px flex flex-col gap-4 rounded-xl border px-4 py-3.5">
        <div className="flex items-center justify-between gap-4">
          <h2 className="text-muted-foreground text-xs">Budgets</h2>
          {available.length > 0 && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setEditing('new')
              }}
            >
              Add budget
            </Button>
          )}
        </div>
        {byInterval.size === 0 ? (
          <p className="text-muted-foreground text-sm">No budgets set.</p>
        ) : (
          intervals.map(({ value, label }) => {
            const budget = byInterval.get(value)
            return (
              budget && (
                <BudgetRow
                  key={budget.id}
                  budget={budget}
                  label={label}
                  onEdit={() => {
                    setEditing(budget)
                  }}
                />
              )
            )
          })
        )}
      </div>
      <p className="text-muted-foreground px-4 py-2 text-xs">
        Model calls only, not machines · Days, weeks, and months are UTC; weeks start Monday
      </p>
      {editing && (
        <BudgetDialog
          orgId={orgId}
          projectId={projectId}
          scope={scope}
          budget={editing === 'new' ? undefined : editing}
          available={available}
          onClose={() => {
            setEditing(null)
          }}
        />
      )}
    </section>
  )
}

function BudgetRow({
  budget,
  label,
  onEdit,
}: {
  budget: Budget
  label: string
  onEdit: () => void
}) {
  const spent = budget.usage?.amount_usd ?? '0'
  const reached = spendReachesBudget(spent, budget.amount_usd)
  const percent = Math.floor(Math.min(100, (Number(spent) / Number(budget.amount_usd)) * 100))
  const resets = resetDate(budget.interval)
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center justify-between gap-4">
        <p className="flex min-w-0 flex-wrap items-baseline gap-x-1.5">
          <span className="text-sm font-medium">{label}</span>
          <span className="font-semibold tabular-nums">{formatUsd(spent)}</span>
          <span className="text-muted-foreground text-sm">of {formatUsd(budget.amount_usd)}</span>
        </p>
        <Button size="sm" variant="ghost" aria-label={`Edit ${label} budget`} onClick={onEdit}>
          Edit
        </Button>
      </div>
      <div
        className="bg-muted h-1.5 overflow-hidden rounded-full"
        role="progressbar"
        aria-label={`${label} budget used`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent}
      >
        <div
          className={cn('h-full rounded-full', reached ? 'bg-destructive' : 'bg-primary')}
          style={{ width: `${String(percent)}%` }}
        />
      </div>
      <div className="text-muted-foreground flex justify-between gap-4 text-xs">
        <p className={cn(reached && 'text-destructive')}>
          {reached
            ? `New model calls are blocked until the budget is raised${resets ? ` or resets ${resets}` : ''}`
            : resets
              ? `Resets ${resets}`
              : 'Never resets'}
        </p>
        <p className={cn('tabular-nums', reached && 'text-destructive font-medium')}>
          {reached ? 'Budget reached' : `${String(percent)}% used`}
        </p>
      </div>
    </div>
  )
}

const resetDateFormatter = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
  timeZone: 'UTC',
})

function resetDate(interval: BudgetInterval, now = new Date()) {
  const year = now.getUTCFullYear()
  const month = now.getUTCMonth()
  const day = now.getUTCDate()
  switch (interval) {
    case 'day':
      return resetDateFormatter.format(Date.UTC(year, month, day + 1))
    case 'week':
      return resetDateFormatter.format(Date.UTC(year, month, day + 7 - ((now.getUTCDay() + 6) % 7)))
    case 'month':
      return resetDateFormatter.format(Date.UTC(year, month + 1, 1))
    case 'lifetime':
      return null
  }
}

function BudgetDialog({
  orgId,
  projectId,
  scope,
  budget,
  available,
  onClose,
}: {
  orgId: string
  projectId: string | undefined
  scope: Scope
  budget: Budget | undefined
  available: typeof intervals
  onClose: () => void
}) {
  const create = useCreateBudget(orgId)
  const update = useUpdateBudget(orgId)
  const remove = useDeleteBudget(orgId)
  const [interval, setBudgetInterval] = useState<BudgetInterval>(
    budget?.interval ??
      (available.some(({ value }) => value === 'month') ? 'month' : (available[0]?.value ?? 'day')),
  )
  const [value, setValue] = useState(budget?.amount_usd ?? '')
  const [status, setStatus] = useState<SubmitStatus>(idle)
  const isSubmitting = status.phase === 'submitting'
  const errorMessage = statusError(status)
  const trimmed = value.trim()
  const valid = zBudgetAmount.safeParse(trimmed).success && Number(trimmed) > 0
  const label = intervals.find((option) => option.value === interval)?.label ?? interval

  async function run(action: () => Promise<void>, fallback: string) {
    setStatus(submitting)
    try {
      await action()
    } catch (err) {
      setStatus(submitError(err, fallback))
      return
    }
    setStatus(idle)
    onClose()
  }

  function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!valid) return
    void run(async () => {
      if (budget) {
        await update.mutateAsync({ budgetID: budget.id, amount_usd: trimmed })
        return
      }
      await create.mutateAsync({
        scope: projectId ? { type: 'project', project_id: projectId } : { type: 'org' },
        interval,
        amount_usd: trimmed,
      })
    }, 'Could not save the budget')
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{budget ? `${label} budget` : 'Add budget'}</DialogTitle>
          <DialogDescription>
            New model calls in this {scope} stop once model spend for the interval reaches the
            budget. Calls already running finish, so spend can exceed the budget by their cost.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit}>
          <FieldGroup>
            {!budget && (
              <Field>
                <FieldLabel htmlFor="budget-interval">Interval</FieldLabel>
                <Select
                  value={interval}
                  onValueChange={(next) => {
                    const option = available.find((item) => item.value === next)
                    if (option) setBudgetInterval(option.value)
                  }}
                >
                  <SelectTrigger id="budget-interval" className="w-full">
                    <SelectValue>{label}</SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {available.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        {option.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            )}
            <Field>
              <FieldLabel htmlFor="budget-usd">Budget (USD)</FieldLabel>
              <Input
                id="budget-usd"
                inputMode="decimal"
                placeholder="500"
                value={value}
                onChange={(event) => {
                  setValue(event.target.value)
                }}
              />
              <FieldDescription>
                Spend uses your model prices when set, otherwise the provider-reported cost or a
                list-price estimate.
              </FieldDescription>
            </Field>
            {errorMessage && <p className="text-destructive text-sm">{errorMessage}</p>}
            <DialogFooter>
              {budget && (
                <Button
                  type="button"
                  variant="outline"
                  disabled={isSubmitting}
                  onClick={() => {
                    void run(() => remove.mutateAsync(budget.id), 'Could not remove the budget')
                  }}
                >
                  Remove budget
                </Button>
              )}
              <Button type="submit" disabled={isSubmitting || !valid} loading={isSubmitting}>
                Save
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
