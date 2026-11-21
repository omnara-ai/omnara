import { useQuery } from '@tanstack/react-query'

import { Field, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { webConfigQuery } from '@/lib/web-config'

import { machinePoolCreatableProviders, machinePoolProviderLabel } from './MachinePoolDialogState'
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
  const { data: webConfig } = useQuery(webConfigQuery)
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
          {machinePoolCreatableProviders(webConfig?.dockerPoolsEnabled === true).map((option) => (
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
