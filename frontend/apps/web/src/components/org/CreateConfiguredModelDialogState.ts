import type { DiscoveredProviderModel, ModelApiFormat, ModelProviderConfig } from '@omnara/sdk'

import { resourceNameSuggestion, resourceNameValid } from '@/lib/resource-name'

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
  | 'defaultMaxOutputTokens'

export function configuredModelSuggestedName(providerModelSlug: string) {
  return resourceNameSuggestion(providerModelSlug, 'Configured model')
}

function isGeneratedName(values: ConfiguredModelFormValues) {
  return values.name === configuredModelSuggestedName(values.providerModelSlug)
}

export function discoveredModelPrefill(
  values: ConfiguredModelFormValues,
  model: DiscoveredProviderModel,
): [DiscoveredModelPrefillField, string][] {
  const updates: [DiscoveredModelPrefillField, string][] = [['providerModelSlug', model.slug]]
  if (values.name === '' || isGeneratedName(values)) {
    updates.push(['name', configuredModelSuggestedName(model.slug)])
  }
  updates.push([
    'contextWindowTokens',
    model.context_window_tokens === undefined ? '' : String(model.context_window_tokens),
  ])
  updates.push([
    'maxOutputTokens',
    model.max_output_tokens === undefined ? '' : String(model.max_output_tokens),
  ])
  return updates
}

export function providerChangeReset(
  values: ConfiguredModelFormValues,
): [DiscoveredModelPrefillField, string][] {
  const updates: [DiscoveredModelPrefillField, string][] = [
    ['providerModelSlug', ''],
    ['contextWindowTokens', ''],
    ['maxOutputTokens', ''],
    ['defaultMaxOutputTokens', ''],
  ]
  if (isGeneratedName(values)) {
    updates.push(['name', ''])
  }
  return updates
}

export function configuredModelTokenLimitsError(
  values: Pick<
    ConfiguredModelFormValues,
    'contextWindowTokens' | 'maxOutputTokens' | 'defaultMaxOutputTokens'
  >,
  apiFormat: ModelApiFormat,
) {
  const contextWindowTokensValue = Number(values.contextWindowTokens)
  const maxOutputTokensValue = Number(values.maxOutputTokens)
  const defaultMaxOutputTokensValue = Number(values.defaultMaxOutputTokens)
  if (!Number.isInteger(contextWindowTokensValue) || contextWindowTokensValue < 2) {
    return 'Context window must be a whole number greater than one.'
  }
  if (apiFormat === 'anthropic-messages' && values.maxOutputTokens === '') {
    return 'Max output is required for Anthropic Messages.'
  }
  if (
    values.maxOutputTokens !== '' &&
    (!Number.isInteger(maxOutputTokensValue) ||
      maxOutputTokensValue <= 0 ||
      maxOutputTokensValue >= contextWindowTokensValue)
  ) {
    return 'Max output must be a positive whole number below the context window.'
  }
  if (
    values.defaultMaxOutputTokens !== '' &&
    (!Number.isInteger(defaultMaxOutputTokensValue) ||
      defaultMaxOutputTokensValue <= 0 ||
      defaultMaxOutputTokensValue >= contextWindowTokensValue ||
      (values.maxOutputTokens !== '' && defaultMaxOutputTokensValue > maxOutputTokensValue))
  ) {
    return 'Default output must be a positive whole number below the context window and no greater than max output.'
  }
  return ''
}

export function configuredModelFormValid(
  values: ConfiguredModelFormValues,
  provider: ModelProviderConfig | undefined,
) {
  return (
    provider !== undefined &&
    resourceNameValid(values.name) &&
    values.providerModelSlug.trim() !== '' &&
    !configuredModelTokenLimitsError(values, provider.api_format)
  )
}
