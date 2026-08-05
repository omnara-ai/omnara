import { Field, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { machinePoolProviders } from './CreateMachinePoolDialogState'

export function MachinePoolProviderSelect({
  value,
  onValueChange,
}: {
  value: string
  onValueChange: (provider: string) => void
}) {
  return (
    <Field>
      <FieldLabel htmlFor="mpool-provider">Provider</FieldLabel>
      <Select value={value} onValueChange={onValueChange}>
        <SelectTrigger id="mpool-provider" className="w-full">
          <SelectValue />
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
