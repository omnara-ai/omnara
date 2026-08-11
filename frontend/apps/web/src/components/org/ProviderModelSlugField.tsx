import { useModelProvider } from '@omnara/react'
import type { DiscoveredProviderModel, ModelProviderConfig } from '@omnara/sdk'

import {
  Combobox,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxInput,
  ComboboxItem,
  ComboboxList,
  ComboboxLoading,
} from '@/components/ui/combobox'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'

function modelLabel(model: DiscoveredProviderModel) {
  return model.display_name && model.display_name !== model.slug
    ? `${model.display_name} (${model.slug})`
    : model.slug
}

/**
 * Provider model slug field with autocomplete over the provider's discovered
 * models. Typing always commits the raw text, so slugs the provider does not
 * advertise (or a failed discovery probe) never block the form.
 */
export function ProviderModelSlugField({
  id,
  orgId,
  provider,
  enabled,
  value,
  disabled,
  onChange,
  onSelect,
}: {
  id: string
  orgId: string
  provider: ModelProviderConfig | undefined
  enabled: boolean
  value: string
  disabled?: boolean
  onChange: (slug: string) => void
  onSelect: (model: DiscoveredProviderModel) => void
}) {
  const providerQuery = useModelProvider(orgId, provider?.id ?? '', { enabled })
  const discovery = providerQuery.data?.model_discovery
  const models = discovery?.status === 'ok' ? (discovery.models ?? []) : []
  return (
    <Field>
      <FieldLabel htmlFor={id}>Provider model slug</FieldLabel>
      <Combobox
        items={models}
        inputValue={value}
        onInputValueChange={(nextValue, eventDetails) => {
          if (eventDetails.reason === 'input-change') {
            onChange(nextValue)
          }
        }}
        value={models.find((model) => model.slug === value) ?? null}
        onValueChange={(model: DiscoveredProviderModel | null) => {
          if (model) {
            onSelect(model)
          }
        }}
        itemToStringLabel={modelLabel}
        itemToStringValue={(model: DiscoveredProviderModel) => model.slug}
        isItemEqualToValue={(model: DiscoveredProviderModel, other: DiscoveredProviderModel) =>
          model.slug === other.slug
        }
        disabled={disabled}
      >
        <ComboboxInput id={id} required placeholder="gpt-5.5" />
        <ComboboxContent>
          <ComboboxEmpty>
            {providerQuery.isPending ? null : 'No detected models match. Enter the slug manually.'}
          </ComboboxEmpty>
          <ComboboxList>
            {(model: DiscoveredProviderModel) => (
              <ComboboxItem key={model.slug} value={model}>
                {modelLabel(model)}
              </ComboboxItem>
            )}
          </ComboboxList>
          {providerQuery.isPending && <ComboboxLoading label="Detecting models…" />}
        </ComboboxContent>
      </Combobox>
      {discovery?.status === 'failed' && (
        <FieldDescription>
          Model detection failed for this provider; enter the slug manually.
        </FieldDescription>
      )}
    </Field>
  )
}
