import { Field, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import {
  type IntegrationCredentialsDraft,
  integrationFields,
  integrationProviderLabel,
  integrationProviders,
  newIntegrationCredentials,
} from './integrationCredentials'
import { StoredValueField } from './SecretValueEditor'

export function IntegrationCredentialsFields({
  value,
  onChange,
  fixedProvider = false,
}: {
  value: IntegrationCredentialsDraft
  onChange: (value: IntegrationCredentialsDraft) => void
  fixedProvider?: boolean
}) {
  return (
    <>
      {!fixedProvider && (
        <Field>
          <FieldLabel htmlFor="credential-provider">Provider</FieldLabel>
          <Select
            value={value.provider}
            onValueChange={(provider) => {
              const option = integrationProviders.find((item) => item.value === provider)
              if (option) onChange(newIntegrationCredentials(option.value))
            }}
          >
            <SelectTrigger id="credential-provider">
              <SelectValue>{integrationProviderLabel(value.provider)}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {integrationProviders.map((provider) => (
                <SelectItem key={provider.value} value={provider.value}>
                  {provider.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      )}
      {integrationFields(value.provider).map((field) => (
        <StoredValueField
          key={field.value}
          label={field.label}
          required
          stored={false}
          multiline={field.value === 'private_key'}
          value={value.values[field.value]}
          onChange={(next) => {
            onChange({ ...value, values: { ...value.values, [field.value]: next } })
          }}
          onUndo={() => {
            onChange({
              ...value,
              values: Object.fromEntries(
                Object.entries(value.values).filter(([key]) => key !== field.value),
              ),
            })
          }}
        />
      ))}
    </>
  )
}
