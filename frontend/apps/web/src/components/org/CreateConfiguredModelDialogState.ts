import type { ModelProviderConfig } from '@omnara/sdk'

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
