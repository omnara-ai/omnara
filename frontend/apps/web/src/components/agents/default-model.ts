import type { ConfiguredModelSummary } from '@omnara/sdk'

type ModelIdentity = Pick<ConfiguredModelSummary, 'provider_config' | 'name'>

export function selectDefaultModel<T extends ModelIdentity>(models: T[]): T | undefined {
  return (
    models.find(
      (model) =>
        model.provider_config === 'omnara-openlake' && model.name === 'google/gemma-4-31b-it',
    ) ??
    models.find((model) => model.provider_config === 'omnara-openlake') ??
    models.find(
      (model) =>
        model.provider_config === 'omnara-openrouter' && model.name === 'openai/gpt-6.1-sol',
    ) ??
    models[0]
  )
}
