import type { ComponentProps, ReactNode } from 'react'

import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

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
  error?: ReactNode
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
  return (
    <Field>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <Input
        {...inputProps}
        id={id}
        value={value}
        onChange={(event) => {
          onValueChange(event.target.value)
        }}
      />
      {error}
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
