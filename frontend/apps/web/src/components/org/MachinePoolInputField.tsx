import type { ComponentProps } from 'react'

import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
  RequiredFieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { shownFieldError } from './machinePoolValidation'

type MachinePoolInputFieldProps = Omit<
  ComponentProps<typeof Input>,
  'id' | 'value' | 'onChange'
> & {
  id: string
  label: string
  value: string
  onValueChange: (value: string) => void
  description?: string
  descriptionHref?: string
  /** Why the value is invalid; an empty field isn't flagged, it only blocks progress. */
  error?: string
}

export function MachinePoolInputField({
  id,
  label,
  value,
  onValueChange,
  description,
  descriptionHref,
  error,
  ...inputProps
}: MachinePoolInputFieldProps) {
  const shownError = shownFieldError(value, error)
  return (
    <Field>
      {inputProps.required ? (
        <RequiredFieldLabel htmlFor={id}>{label}</RequiredFieldLabel>
      ) : (
        <FieldLabel htmlFor={id}>{label}</FieldLabel>
      )}
      <Input
        {...inputProps}
        id={id}
        value={value}
        aria-invalid={shownError !== undefined || undefined}
        onChange={(event) => {
          onValueChange(event.target.value)
        }}
      />
      {shownError && <FieldError>{shownError}</FieldError>}
      {description && (
        <FieldDescription>
          {description}{' '}
          {descriptionHref && (
            <a
              href={descriptionHref}
              target="_blank"
              rel="noreferrer"
              className="text-foreground underline underline-offset-2"
            >
              View {label.toLowerCase()} documentation
            </a>
          )}
        </FieldDescription>
      )}
    </Field>
  )
}
