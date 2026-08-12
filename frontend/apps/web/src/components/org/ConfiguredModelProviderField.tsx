import type { ModelProviderConfig } from '@omnara/sdk'

import { Field, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

export function ConfiguredModelProviderField({
  providers,
  value,
  onChange,
}: {
  providers: ModelProviderConfig[]
  /** Provider id; '' falls back to the first available provider. */
  value: string
  onChange: (providerId: string) => void
}) {
  return (
    <Field>
      <FieldLabel htmlFor="cm-provider">Model provider</FieldLabel>
      <Select
        value={(providers.find((item) => item.id === value) ?? providers[0])?.id ?? ''}
        onValueChange={onChange}
      >
        <SelectTrigger id="cm-provider" className="w-full">
          <SelectValue placeholder="Select a provider" />
        </SelectTrigger>
        <SelectContent>
          {providers.map((option) => (
            <SelectItem key={option.id} value={option.id}>
              {option.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </Field>
  )
}
