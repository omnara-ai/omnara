import type { ModelApiFormat } from '@omnara/sdk'

import {
  type SecretRow,
  secretRowsValid,
  type TextRow,
  textRowsValid,
} from '@/components/key-value/keyValueRows'
import { resourceNameValid } from '@/lib/resource-name'

export const modelProviderOptions = [
  {
    value: 'openai',
    label: 'OpenAI',
    keyPlaceholder: 'sk-…',
    endpoint: { baseUrl: 'https://api.openai.com/v1', apiFormat: 'openai-responses' },
  },
  {
    value: 'openrouter',
    label: 'OpenRouter',
    keyPlaceholder: 'sk-or-v1-…',
    endpoint: { baseUrl: 'https://openrouter.ai/api/v1', apiFormat: 'openai-chat-completions' },
  },
  {
    value: 'anthropic',
    label: 'Anthropic',
    keyPlaceholder: 'sk-ant-…',
    endpoint: { baseUrl: 'https://api.anthropic.com/v1', apiFormat: 'anthropic-messages' },
  },
  {
    value: 'bedrock',
    label: 'Amazon Bedrock',
    keyPlaceholder: 'Bedrock API key',
    endpoint: undefined,
  },
  {
    value: 'custom',
    label: 'Custom endpoint',
    keyPlaceholder: 'API key',
    endpoint: undefined,
  },
] as const satisfies readonly {
  value: string
  label: string
  keyPlaceholder: string
  /** The endpoint a preset configures server-side; Bedrock and custom derive theirs from fields. */
  endpoint: { baseUrl: string; apiFormat: ModelApiFormat } | undefined
}[]

export type ModelProviderOption = (typeof modelProviderOptions)[number]['value']

export const apiFormatOptions = [
  { value: 'openai-chat-completions', label: 'OpenAI Chat Completions' },
  { value: 'openai-responses', label: 'OpenAI Responses' },
  { value: 'anthropic-messages', label: 'Anthropic Messages' },
] satisfies { value: ModelApiFormat; label: string }[]

export function apiFormatLabel(value: ModelApiFormat) {
  return apiFormatOptions.find((option) => option.value === value)?.label ?? value
}

export const baseUrlPattern = /^https?:\/\/\S+$/i

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

export const bedrockAuthOptions = [
  { value: 'api-key', label: 'API key' },
  { value: 'sigv4', label: 'AWS credentials (SigV4)' },
] as const

export type BedrockAuth = (typeof bedrockAuthOptions)[number]['value']

export const awsRegionPattern = /^[a-z0-9]+(?:-[a-z0-9]+)+-\d+$/

export function modelProviderOption(value: ModelProviderOption) {
  return modelProviderOptions.find((option) => option.value === value) ?? modelProviderOptions[0]
}

export function bedrockBaseUrl(api: BedrockAPI, region: string) {
  return `https://bedrock-mantle.${region.trim()}.api.aws${bedrockAPIOption(api).basePath}`
}

export function bedrockAPIOption(value: BedrockAPI) {
  return bedrockAPIOptions.find((option) => option.value === value) ?? bedrockAPIOptions[0]
}

export function bedrockAuthOption(value: BedrockAuth) {
  return bedrockAuthOptions.find((option) => option.value === value) ?? bedrockAuthOptions[0]
}

export interface CreateModelProviderFormValues {
  name: string
  provider: ModelProviderOption
  bedrockAPI: BedrockAPI
  bedrockAuth: BedrockAuth
  region: string
  apiFormat: ModelApiFormat
  baseUrl: string
  secretId: string
  headerRows: TextRow[]
  secretHeaderRows: SecretRow[]
}

export const createModelProviderFormDefaults: CreateModelProviderFormValues = {
  name: '',
  provider: 'openai',
  bedrockAPI: 'chat-completions-v1',
  bedrockAuth: 'api-key',
  region: 'us-west-2',
  apiFormat: 'openai-responses',
  baseUrl: 'https://api.openai.com/v1',
  secretId: '',
  headerRows: [],
  secretHeaderRows: [],
}

export function createModelProviderFormValid(values: CreateModelProviderFormValues) {
  return (
    resourceNameValid(values.name) &&
    values.secretId !== '' &&
    (values.provider !== 'bedrock' || awsRegionPattern.test(values.region.trim())) &&
    (values.provider === 'bedrock' || baseUrlPattern.test(values.baseUrl.trim())) &&
    textRowsValid(values.headerRows) &&
    secretRowsValid(values.secretHeaderRows)
  )
}

export function providerSecretName(provider: ModelProviderOption) {
  return `${provider}-api-key`
}

/** The endpoint fields a provider starts with: its preset endpoint, or blank for a custom one. */
export function modelProviderEndpointDefaults(
  provider: ModelProviderOption,
): Pick<CreateModelProviderFormValues, 'baseUrl' | 'apiFormat'> {
  const endpoint = modelProviderOption(provider).endpoint
  return endpoint
    ? { baseUrl: endpoint.baseUrl, apiFormat: endpoint.apiFormat }
    : { baseUrl: '', apiFormat: 'openai-chat-completions' }
}
