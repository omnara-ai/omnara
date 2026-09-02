import { FieldError } from '@/components/ui/field'
import { resourceNameError } from '@/lib/resource-name'

export function ResourceNameFieldError({
  value,
  fieldLabel,
  showRequired = false,
}: {
  value: string
  fieldLabel?: string
  showRequired?: boolean
}) {
  if (!showRequired && value === '') return null
  return <FieldError>{resourceNameError(value, fieldLabel)}</FieldError>
}
