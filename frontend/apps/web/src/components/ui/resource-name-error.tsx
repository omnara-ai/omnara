import { FieldError } from '@/components/ui/field'
import { resourceNameError } from '@/lib/resource-name'

export function ResourceNameFieldError({
  value,
  validate = true,
  fieldLabel,
  showRequired = false,
}: {
  value: string
  validate?: boolean
  fieldLabel?: string
  showRequired?: boolean
}) {
  if (!validate || (!showRequired && value === '')) return null
  return <FieldError>{resourceNameError(value, fieldLabel)}</FieldError>
}
