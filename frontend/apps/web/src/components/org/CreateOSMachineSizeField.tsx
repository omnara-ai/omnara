import { useCreateOSMachineSizes } from '@omnara/react'
import type { CreateOsMachineSize } from '@omnara/sdk'

import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

function machineSizeLabel(machineSize: CreateOsMachineSize) {
  const memoryGb = machineSize.memory_mb / 1024
  return `${machineSize.id} - ${machineSize.vcpu} vCPU, ${memoryGb} GB`
}

export function CreateOSMachineSizeField({
  orgId,
  enabled,
  secretId,
  value,
  onSelect,
}: {
  orgId: string
  enabled: boolean
  secretId: string
  value: string
  onSelect: (machineSize: CreateOsMachineSize) => void
}) {
  const machineSizesQuery = useCreateOSMachineSizes(orgId, secretId, {
    enabled: enabled && secretId !== '',
  })
  const machineSizes = machineSizesQuery.data?.data ?? []
  const selected = machineSizes.find((candidate) => candidate.id === value)

  return (
    <Field>
      <FieldLabel htmlFor="mpool-image">Machine size</FieldLabel>
      <Select
        value={value}
        disabled={secretId === '' || machineSizesQuery.isPending}
        onValueChange={(machineSizeId) => {
          const machineSize = machineSizes.find((candidate) => candidate.id === machineSizeId)
          if (machineSize) onSelect(machineSize)
        }}
      >
        <SelectTrigger id="mpool-image" className="w-full">
          <SelectValue
            placeholder={secretId === '' ? 'Select an API token first' : 'Select a machine size'}
          >
            {selected ? machineSizeLabel(selected) : value}
          </SelectValue>
        </SelectTrigger>
        <SelectContent>
          {machineSizes.map((machineSize) => (
            <SelectItem key={machineSize.id} value={machineSize.id}>
              {machineSizeLabel(machineSize)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {machineSizesQuery.isError && (
        <FieldDescription>
          Could not load machine sizes. Check the selected CreateOS API token.
        </FieldDescription>
      )}
    </Field>
  )
}
