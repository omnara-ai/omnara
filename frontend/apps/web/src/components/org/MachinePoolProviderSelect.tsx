import { Field, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { machinePoolProviderLabel, machinePoolProviders } from './MachinePoolDialogState'

export function MachinePoolProviderSelect({
  value,
  disabled = false,
  onValueChange,
}: {
  value: string
  disabled?: boolean
  onValueChange: (provider: string) => void
}) {
  return (
    <Field>
      <FieldLabel htmlFor="mpool-provider">Provider</FieldLabel>
      <Select value={value} disabled={disabled} onValueChange={onValueChange}>
        <SelectTrigger id="mpool-provider" className="w-full">
          <SelectValue>{machinePoolProviderLabel(value)}</SelectValue>
        </SelectTrigger>
        <SelectContent>
          {machinePoolProviders.map((option) => (
            <SelectItem key={option.value} value={option.value}>
              {option.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </Field>
  )
}
