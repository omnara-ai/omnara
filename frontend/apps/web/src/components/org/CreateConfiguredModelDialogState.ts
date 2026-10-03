import type { CreateConfiguredModelRequest, DiscoveredProviderModel } from '@omnara/sdk'

import { resourceNameError, resourceNameSuggestion } from '@/lib/resource-name'

/** A model selected for creation, with editable fields kept as input drafts. */
export interface ConfiguredModelDraft {
  slug: string
  name: string
  contextWindowTokens: string
  maxOutputTokens: string
}

export function configuredModelSuggestedName(providerModelSlug: string) {
  return resourceNameSuggestion(providerModelSlug, 'Configured model')
}

/** The slug's suggested name, numbered past any name already taken on the provider. */
function uniqueConfiguredModelName(providerModelSlug: string, takenNames: ReadonlySet<string>) {
  let name = configuredModelSuggestedName(providerModelSlug)
  for (let suffix = 2; takenNames.has(name); suffix++) {
    name = configuredModelSuggestedName(`${providerModelSlug}-${String(suffix)}`)
  }
  return name
}

/**
 * A draft prefilled from the provider's catalog or an existing configuration of the same
 * slug: a name not already taken and the reported token limits.
 */
export function configuredModelDraft(
  model: Pick<DiscoveredProviderModel, 'slug' | 'context_window_tokens' | 'max_output_tokens'>,
  takenNames: ReadonlySet<string> = new Set(),
): ConfiguredModelDraft {
  return {
    slug: model.slug,
    name: uniqueConfiguredModelName(model.slug, takenNames),
    contextWindowTokens:
      model.context_window_tokens === undefined ? '' : String(model.context_window_tokens),
    maxOutputTokens: model.max_output_tokens === undefined ? '' : String(model.max_output_tokens),
  }
}

export function configuredModelTokenLimitsError(values: {
  contextWindowTokens: string
  maxOutputTokens: string
  defaultMaxOutputTokens: string
}) {
  const contextWindowTokensValue = Number(values.contextWindowTokens)
  const maxOutputTokensValue = Number(values.maxOutputTokens)
  const defaultMaxOutputTokensValue = Number(values.defaultMaxOutputTokens)
  if (!Number.isInteger(contextWindowTokensValue) || contextWindowTokensValue < 2) {
    return 'Context window must be a whole number greater than one.'
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

/**
 * Why a draft cannot be created yet, or '' when it is ready. takenNames holds the names
 * of the provider's models and the other drafts, which must not repeat.
 */
export function configuredModelDraftError(
  draft: ConfiguredModelDraft,
  takenNames: ReadonlySet<string> = new Set(),
) {
  const nameError = resourceNameError(draft.name)
  if (nameError) return nameError
  if (takenNames.has(draft.name)) return 'Another model on this provider already uses this name.'
  if (draft.contextWindowTokens.trim() === '') {
    return 'Enter the context window; the provider did not report it.'
  }
  return configuredModelTokenLimitsError({ ...draft, defaultMaxOutputTokens: '' })
}

export function configuredModelDraftRequest(
  draft: ConfiguredModelDraft,
): CreateConfiguredModelRequest {
  const request: CreateConfiguredModelRequest = {
    name: draft.name,
    provider_model_slug: draft.slug,
    context_window_tokens: Number(draft.contextWindowTokens),
    supports_tools: true,
    supports_reasoning: false,
  }
  if (draft.maxOutputTokens.trim() !== '') request.max_output_tokens = Number(draft.maxOutputTokens)
  return request
}

export function discoveredModelMatches(model: DiscoveredProviderModel, search: string) {
  const query = search.trim().toLowerCase()
  if (query === '') return true
  return (
    model.slug.toLowerCase().includes(query) ||
    (model.display_name?.toLowerCase().includes(query) ?? false)
  )
}
