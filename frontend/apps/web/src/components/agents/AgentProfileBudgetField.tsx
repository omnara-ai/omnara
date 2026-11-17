import { useId } from 'react'

import type { ProfileBudget } from '@/components/agents/useProfileBudget'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { BudgetIntervalSelect } from '@/components/usage/BudgetIntervalSelect'

export function AgentProfileBudgetField({
  budget,
  submitting,
  saved,
}: {
  budget: ProfileBudget
  submitting: boolean
  saved: boolean
}) {
  const id = useId()
  const { draft, setDraft, visible, valid } = budget
  if (!visible) return null
  const disabled = submitting || saved
  return (
    <FieldGroup className="bg-card rounded-xl border px-4 py-4 sm:px-5">
      <Field>
        <FieldLabel htmlFor={id}>Budget (USD)</FieldLabel>
        <div className="mt-2 flex gap-2">
          <div className="w-32 shrink-0">
            <BudgetIntervalSelect
              id={`${id}-interval`}
              ariaLabel="Budget interval"
              value={draft.interval}
              disabled={disabled}
              onChange={(interval) => {
                setDraft({ ...draft, interval })
              }}
            />
          </div>
          <Input
            id={id}
            inputMode="decimal"
            placeholder="No budget"
            value={draft.amount}
            disabled={disabled}
            aria-invalid={!valid}
            onChange={(event) => {
              setDraft({ ...draft, amount: event.target.value })
            }}
          />
        </div>
        {valid ? (
          <FieldDescription>
            {saved
              ? 'This profile already exists. Change its budget on its Usage tab.'
              : 'Caps combined model spend for every agent launched from this profile, including subagents.'}
          </FieldDescription>
        ) : (
          <FieldError>Enter an amount above 0 in US dollars.</FieldError>
        )}
      </Field>
    </FieldGroup>
  )
}
