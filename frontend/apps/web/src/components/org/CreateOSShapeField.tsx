import { useCreateOSShapes } from '@omnara/react'
import type { CreateOsShape } from '@omnara/sdk'

import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

function shapeLabel(shape: CreateOsShape) {
  const memoryGb = shape.memory_mb / 1024
  return `${shape.id} — ${shape.vcpu} vCPU, ${memoryGb} GB`
}

export function CreateOSShapeField({
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
  onSelect: (shape: CreateOsShape) => void
}) {
  const shapesQuery = useCreateOSShapes(orgId, secretId, {
    enabled: enabled && secretId !== '',
  })
  const shapes = shapesQuery.data?.data ?? []
  const selected = shapes.find((shape) => shape.id === value)

  return (
    <Field>
      <FieldLabel htmlFor="mpool-image">Shape</FieldLabel>
      <Select
        value={value}
        disabled={secretId === '' || shapesQuery.isPending}
        onValueChange={(shapeId) => {
          const shape = shapes.find((candidate) => candidate.id === shapeId)
          if (shape) onSelect(shape)
        }}
      >
        <SelectTrigger id="mpool-image" className="w-full">
          <SelectValue placeholder={secretId === '' ? 'Select an API token first' : 'Select shape'}>
            {selected ? shapeLabel(selected) : value}
          </SelectValue>
        </SelectTrigger>
        <SelectContent>
          {shapes.map((shape) => (
            <SelectItem key={shape.id} value={shape.id}>
              {shapeLabel(shape)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {shapesQuery.isError && (
        <FieldDescription>
          Could not load shapes. Check the selected CreateOS API token.
        </FieldDescription>
      )}
    </Field>
  )
}
