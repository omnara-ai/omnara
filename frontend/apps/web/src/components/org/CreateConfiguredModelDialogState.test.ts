import { describe, expect, it } from 'vitest'

import {
  configuredModelFormDefaults,
  configuredModelSuggestedName,
  discoveredModelPrefill,
  providerChangeReset,
} from './CreateConfiguredModelDialogState'

const discoveredModel = {
  slug: 'nvidia/nemotron-3.5-light',
  context_window_tokens: 262_144,
  max_output_tokens: 16_384,
}

describe('generated configured model names', () => {
  it('uses the model slug when it is a valid name', () => {
    expect(discoveredModelPrefill(configuredModelFormDefaults, discoveredModel)).toContainEqual([
      'name',
      discoveredModel.slug,
    ])
  })

  it('recognizes shortened suggestions and refreshes them with a new model', () => {
    const firstSlug = `first-model-${'a'.repeat(50)}`
    const secondSlug = `second-model-${'b'.repeat(49)}`
    const values = {
      ...configuredModelFormDefaults,
      name: configuredModelSuggestedName(firstSlug),
      providerModelSlug: firstSlug,
    }

    const updates = discoveredModelPrefill(values, { slug: secondSlug })
    expect(updates).toContainEqual(['name', configuredModelSuggestedName(secondSlug)])
    expect(providerChangeReset(values)).toContainEqual(['name', ''])
  })

  it('preserves custom and whitespace-only input', () => {
    const generatedValues = {
      ...configuredModelFormDefaults,
      name: configuredModelSuggestedName('old-model'),
      providerModelSlug: 'old-model',
    }
    const customValues = { ...generatedValues, name: 'My model' }
    const whitespaceValues = { ...generatedValues, name: '   ' }

    expect(discoveredModelPrefill(customValues, discoveredModel)).not.toContainEqual([
      'name',
      configuredModelSuggestedName(discoveredModel.slug),
    ])
    expect(discoveredModelPrefill(whitespaceValues, discoveredModel)).not.toContainEqual([
      'name',
      configuredModelSuggestedName(discoveredModel.slug),
    ])
    expect(providerChangeReset(customValues)).not.toContainEqual(['name', ''])
  })
})
