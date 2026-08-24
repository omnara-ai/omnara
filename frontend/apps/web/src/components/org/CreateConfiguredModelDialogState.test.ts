import { describe, expect, it } from 'vitest'

import {
  configuredModelFormDefaults,
  configuredModelSuggestedName,
  discoveredModelPrefill,
  providerChangeReset,
} from './CreateConfiguredModelDialogState'

const providerName = 'OpenRouter'
const discoveredModel = {
  slug: 'nvidia/nemotron-3.5-light',
  context_window_tokens: 262_144,
  max_output_tokens: 16_384,
}

describe('generated configured model names', () => {
  it('uses the provider and model slug when the combined name fits', () => {
    expect(
      discoveredModelPrefill(providerName, configuredModelFormDefaults, discoveredModel),
    ).toContainEqual(['name', configuredModelSuggestedName(providerName, discoveredModel.slug)])
  })

  it('recognizes shortened suggestions and refreshes them with a new model', () => {
    const firstSlug = `first-model-${'a'.repeat(50)}`
    const secondSlug = `second-model-${'b'.repeat(49)}`
    const values = {
      ...configuredModelFormDefaults,
      name: configuredModelSuggestedName(providerName, firstSlug),
      providerModelSlug: firstSlug,
    }

    const updates = discoveredModelPrefill(providerName, values, { slug: secondSlug })
    expect(updates).toContainEqual(['name', configuredModelSuggestedName(providerName, secondSlug)])
    expect(providerChangeReset(providerName, values)).toContainEqual(['name', ''])
  })

  it('preserves custom and whitespace-only input', () => {
    const generatedValues = {
      ...configuredModelFormDefaults,
      name: configuredModelSuggestedName(providerName, 'old-model'),
      providerModelSlug: 'old-model',
    }
    const customValues = { ...generatedValues, name: 'My model' }
    const whitespaceValues = { ...generatedValues, name: '   ' }

    expect(discoveredModelPrefill(providerName, customValues, discoveredModel)).not.toContainEqual([
      'name',
      configuredModelSuggestedName(providerName, discoveredModel.slug),
    ])
    expect(
      discoveredModelPrefill(providerName, whitespaceValues, discoveredModel),
    ).not.toContainEqual(['name', configuredModelSuggestedName(providerName, discoveredModel.slug)])
    expect(providerChangeReset(providerName, customValues)).not.toContainEqual(['name', ''])
  })
})
