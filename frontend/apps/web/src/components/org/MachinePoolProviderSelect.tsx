import { Field, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { machinePoolProviderLabel, machinePoolProviders } from './MachinePoolDialogState'
import { MachinePoolProviderLogo } from './MachinePoolProviderLogo'

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
          <SelectValue>
            <ProviderOption provider={value} label={machinePoolProviderLabel(value)} />
          </SelectValue>
        </SelectTrigger>
        <SelectContent>
          {machinePoolProviders.map((option) => (
            <SelectItem key={option.value} value={option.value}>
              <ProviderOption provider={option.value} label={option.label} />
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </Field>
  )
}

function ProviderOption({ provider, label }: { provider: string; label: string }) {
  return (
    <span className="flex min-w-0 items-center gap-2">
      <MachinePoolProviderLogo provider={provider} className="size-4 shrink-0" />
      <span className="truncate">{label}</span>
    </span>
  )
}
