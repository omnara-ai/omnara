import type { DiscoveredProviderModel, ModelProviderConfig } from '@omnara/sdk'

export interface ConfiguredModelFormValues {
  /** Provider id; '' falls back to the first available provider. */
  providerId: string
  name: string
  providerModelSlug: string
  contextWindowTokens: string
  maxOutputTokens: string
  defaultMaxOutputTokens: string
  projectGrantIds: string[]
}

export const configuredModelFormDefaults: ConfiguredModelFormValues = {
  providerId: '',
  name: '',
  providerModelSlug: '',
  contextWindowTokens: '',
  maxOutputTokens: '',
  defaultMaxOutputTokens: '',
  projectGrantIds: [],
}

type DiscoveredModelPrefillField =
  | 'name'
  | 'providerModelSlug'
  | 'contextWindowTokens'
  | 'maxOutputTokens'

/**
 * Field updates for picking a discovered model: the slug always, the
 * provider-reported token limits when advertised, and a generated name only
 * while the name field is still empty.
 */
export function discoveredModelPrefill(
  providerName: string | undefined,
  values: ConfiguredModelFormValues,
  model: DiscoveredProviderModel,
): [DiscoveredModelPrefillField, string][] {
  const updates: [DiscoveredModelPrefillField, string][] = [['providerModelSlug', model.slug]]
  if (values.name.trim() === '' && providerName !== undefined) {
    updates.push(['name', `${providerName} - ${model.slug}`])
  }
  if (model.context_window_tokens !== undefined) {
    updates.push(['contextWindowTokens', String(model.context_window_tokens)])
  }
  if (model.max_output_tokens !== undefined) {
    updates.push(['maxOutputTokens', String(model.max_output_tokens)])
  }
  return updates
}

export function configuredModelFormValid(
  values: ConfiguredModelFormValues,
  provider: ModelProviderConfig | undefined,
) {
  const contextWindowTokensValue = Number(values.contextWindowTokens)
  const maxOutputTokensValue = Number(values.maxOutputTokens)
  const defaultMaxOutputTokensValue = Number(values.defaultMaxOutputTokens)
  const maxOutputValid =
    values.maxOutputTokens === '' ||
    (Number.isInteger(maxOutputTokensValue) &&
      maxOutputTokensValue > 0 &&
      maxOutputTokensValue < contextWindowTokensValue)
  const defaultOutputValid =
    values.defaultMaxOutputTokens === '' ||
    (Number.isInteger(defaultMaxOutputTokensValue) &&
      defaultMaxOutputTokensValue > 0 &&
      (values.maxOutputTokens === '' || defaultMaxOutputTokensValue <= maxOutputTokensValue))
  return (
    Boolean(provider) &&
    values.name.trim() !== '' &&
    values.providerModelSlug.trim() !== '' &&
    Number.isInteger(contextWindowTokensValue) &&
    contextWindowTokensValue > 1 &&
    maxOutputValid &&
    defaultOutputValid
  )
}
