import type { CreateConfiguredModelRequest, DiscoveredProviderModel } from '@omnara/sdk'

export const modelProviderOptions = [
  { value: 'openai', label: 'OpenAI', keyPlaceholder: 'sk-…' },
  { value: 'openrouter', label: 'OpenRouter', keyPlaceholder: 'sk-or-v1-…' },
  { value: 'anthropic', label: 'Anthropic', keyPlaceholder: 'sk-ant-…' },
  { value: 'bedrock', label: 'Amazon Bedrock', keyPlaceholder: 'Bedrock API key' },
] as const

export type ModelProviderOption = (typeof modelProviderOptions)[number]['value']

export const bedrockAPIOptions = [
  {
    value: 'chat-completions-v1',
    label: 'Chat Completions (/v1)',
    apiFormat: 'openai-chat-completions',
    basePath: '/v1',
  },
  {
    value: 'responses-openai-v1',
    label: 'Responses (/openai/v1)',
    apiFormat: 'openai-responses',
    basePath: '/openai/v1',
  },
  {
    value: 'anthropic-messages',
    label: 'Anthropic Messages (/anthropic/v1)',
    apiFormat: 'anthropic-messages',
    basePath: '/anthropic/v1',
  },
] as const

export type BedrockAPI = (typeof bedrockAPIOptions)[number]['value']

export const awsRegionPattern = /^[a-z0-9]+(?:-[a-z0-9]+)+-\d+$/

export function modelProviderOption(value: ModelProviderOption) {
  return modelProviderOptions.find((option) => option.value === value) ?? modelProviderOptions[0]
}

export function bedrockAPIOption(value: BedrockAPI) {
  return bedrockAPIOptions.find((option) => option.value === value) ?? bedrockAPIOptions[0]
}

export interface CreateModelProviderFormValues {
  name: string
  provider: ModelProviderOption
  bedrockAPI: BedrockAPI
  region: string
  secretId: string
}

export const createModelProviderFormDefaults: CreateModelProviderFormValues = {
  name: '',
  provider: 'openai',
  bedrockAPI: 'chat-completions-v1',
  region: 'us-west-2',
  secretId: '',
}

export function createModelProviderFormValid(values: CreateModelProviderFormValues) {
  return (
    values.name.trim() !== '' &&
    values.secretId !== '' &&
    (values.provider !== 'bedrock' || awsRegionPattern.test(values.region.trim()))
  )
}

export function providerSecretName(provider: ModelProviderOption) {
  return `${provider}-api-key`
}

export function configuredModelRequestForDiscoveredModel(
  model: DiscoveredProviderModel,
): CreateConfiguredModelRequest {
  if (model.context_window_tokens === undefined || model.context_window_tokens < 2) {
    throw new Error(`No context window was reported for ${model.slug}`)
  }
  return {
    name: model.slug,
    provider_model_slug: model.slug,
    context_window_tokens: model.context_window_tokens,
    ...(model.max_output_tokens === undefined
      ? {}
      : { max_output_tokens: model.max_output_tokens }),
    supports_tools: true,
    supports_reasoning: false,
  }
}
