import { useCreateOSRootFS } from '@omnara/react'

import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

export function CreateOSRootFSField({
  orgId,
  enabled,
  secretId,
  value,
  onChange,
}: {
  orgId: string
  enabled: boolean
  secretId: string
  value: string
  onChange: (value: string) => void
}) {
  const rootFSQuery = useCreateOSRootFS(orgId, secretId, {
    enabled: enabled && secretId !== '',
  })
  const catalog = rootFSQuery.data

  return (
    <Field>
      <FieldLabel htmlFor="mpool-rootfs">RootFS</FieldLabel>
      <Select
        value={value}
        disabled={secretId === '' || rootFSQuery.isPending}
        onValueChange={onChange}
      >
        <SelectTrigger id="mpool-rootfs" className="w-full">
          <SelectValue
            placeholder={secretId === '' ? 'Select an API token first' : 'Select RootFS'}
          >
            {value}
          </SelectValue>
        </SelectTrigger>
        <SelectContent>
          {(catalog?.data ?? []).map((rootFS) => (
            <SelectItem key={rootFS.name} value={rootFS.name}>
              {rootFS.name}
              {rootFS.deprecated ? ' (deprecated)' : ''}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {rootFSQuery.isError && (
        <FieldDescription>
          Could not load root filesystems. Check the selected CreateOS API token.
        </FieldDescription>
      )}
    </Field>
  )
}
