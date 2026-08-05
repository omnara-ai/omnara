import type { CreateConfiguredModelRequest, DiscoveredProviderModel } from '@omnara/sdk'

export const modelProviderPresets = [
  { value: 'openai', label: 'OpenAI', keyPlaceholder: 'sk-…' },
  { value: 'openrouter', label: 'OpenRouter', keyPlaceholder: 'sk-or-v1-…' },
  { value: 'anthropic', label: 'Anthropic', keyPlaceholder: 'sk-ant-…' },
] as const

export type ModelProviderPreset = (typeof modelProviderPresets)[number]['value']

export function modelProviderPresetOption(preset: ModelProviderPreset) {
  return modelProviderPresets.find((option) => option.value === preset) ?? modelProviderPresets[0]
}

export interface CreateModelProviderFormValues {
  name: string
  preset: ModelProviderPreset
  secretId: string
}

export const createModelProviderFormDefaults: CreateModelProviderFormValues = {
  name: '',
  preset: 'openai',
  secretId: '',
}

export function createModelProviderFormValid(values: CreateModelProviderFormValues) {
  return values.name.trim() !== '' && values.secretId !== ''
}

export function presetSecretName(preset: ModelProviderPreset) {
  return `${preset}-api-key`
}

export function configuredModelRequestForDiscoveredModel(
  providerName: string,
  model: DiscoveredProviderModel,
): CreateConfiguredModelRequest {
  if (model.context_window_tokens === undefined || model.context_window_tokens < 2) {
    throw new Error(`No context window was reported for ${model.slug}`)
  }
  return {
    name: `${providerName} - ${model.slug}`,
    provider_model_slug: model.slug,
    context_window_tokens: model.context_window_tokens,
    ...(model.max_output_tokens === undefined
      ? {}
      : { max_output_tokens: model.max_output_tokens }),
    supports_tools: true,
    supports_reasoning: false,
  }
}
