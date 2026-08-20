import type { DiscoveredProviderModel, ModelProviderConfig } from '@omnara/sdk'

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

export function configuredModelSuggestedName(providerName: string, providerModelSlug: string) {
  return resourceNameSuggestion(
    [`${providerName} - ${providerModelSlug}`, providerModelSlug],
    'Configured model',
  )
}

function isGeneratedName(providerName: string, values: ConfiguredModelFormValues) {
  return values.name === configuredModelSuggestedName(providerName, values.providerModelSlug)
}

export function discoveredModelPrefill(
  providerName: string | undefined,
  values: ConfiguredModelFormValues,
  model: DiscoveredProviderModel,
): [DiscoveredModelPrefillField, string][] {
  const updates: [DiscoveredModelPrefillField, string][] = [['providerModelSlug', model.slug]]
  if (providerName !== undefined && (values.name === '' || isGeneratedName(providerName, values))) {
    updates.push(['name', configuredModelSuggestedName(providerName, model.slug)])
  }
  updates.push([
    'contextWindowTokens',
    model.context_window_tokens === undefined ? '' : String(model.context_window_tokens),
  ])
  updates.push([
    'maxOutputTokens',
    model.max_output_tokens === undefined ? '' : String(model.max_output_tokens),
  ])
  if (
    model.max_output_tokens !== undefined &&
    values.defaultMaxOutputTokens !== '' &&
    Number(values.defaultMaxOutputTokens) > model.max_output_tokens
  ) {
    updates.push(['defaultMaxOutputTokens', ''])
  }
  return updates
}

export function providerChangeReset(
  previousProviderName: string | undefined,
  values: ConfiguredModelFormValues,
): [DiscoveredModelPrefillField, string][] {
  const updates: [DiscoveredModelPrefillField, string][] = [
    ['providerModelSlug', ''],
    ['contextWindowTokens', ''],
    ['maxOutputTokens', ''],
    ['defaultMaxOutputTokens', ''],
  ]
  if (previousProviderName !== undefined && isGeneratedName(previousProviderName, values)) {
    updates.push(['name', ''])
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
    resourceNameValid(values.name) &&
    values.providerModelSlug.trim() !== '' &&
    Number.isInteger(contextWindowTokensValue) &&
    contextWindowTokensValue > 1 &&
    maxOutputValid &&
    defaultOutputValid
  )
}
