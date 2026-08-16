import { FieldError } from '@/components/ui/field'
import { resourceNameError } from '@/lib/resource-name'

export function ResourceNameFieldError({
  value,
  validate = true,
  fieldLabel,
}: {
  value: string
  validate?: boolean
  fieldLabel?: string
}) {
  if (!validate || value === '') return null
  return <FieldError>{resourceNameError(value, fieldLabel)}</FieldError>
}
