import { Field, FieldDescription, RequiredFieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { type MachinePoolProvider, machinePoolProviderDefinitions } from './machinePoolProviders'
import {
  machineSizeClassLabel,
  machineSizeClassValue,
  type MachineSizeDrafts,
  machineSizeDrafts,
  machineSizeSelection,
  providerDefaultSizeValue,
} from './machinePoolSizes'

/** Picks one of a provider's fixed machine shapes in place of free CPU and memory inputs. */
export function MachinePoolSizeSelect({
  provider,
  drafts,
  onChange,
}: {
  provider: MachinePoolProvider
  drafts: MachineSizeDrafts
  onChange: (drafts: MachineSizeDrafts) => void
}) {
  const sizes = machinePoolProviderDefinitions[provider].sizes
  if (!sizes) return null
  const selection = machineSizeSelection(provider, drafts)
  const selected = sizes.classes.find((size) => machineSizeClassValue(size) === selection)
  const selectionLabel = selected
    ? machineSizeClassLabel(selected)
    : selection === providerDefaultSizeValue
      ? sizes.providerDefault?.label
      : undefined
  return (
    <Field>
      <RequiredFieldLabel htmlFor="mpool-size">Machine size</RequiredFieldLabel>
      <Select
        value={selection}
        onValueChange={(value) => {
          onChange(machineSizeDrafts(provider, value))
        }}
      >
        <SelectTrigger id="mpool-size" className="w-full">
          <SelectValue placeholder="Choose a size">{selectionLabel}</SelectValue>
        </SelectTrigger>
        <SelectContent>
          {sizes.classes.map((size) => (
            <SelectItem key={machineSizeClassValue(size)} value={machineSizeClassValue(size)}>
              {machineSizeClassLabel(size)}
            </SelectItem>
          ))}
          {sizes.providerDefault && (
            <SelectItem value={providerDefaultSizeValue}>{sizes.providerDefault.label}</SelectItem>
          )}
        </SelectContent>
      </Select>
      {selection === providerDefaultSizeValue && sizes.providerDefault && (
        <FieldDescription>{sizes.providerDefault.description}</FieldDescription>
      )}
    </Field>
  )
}
