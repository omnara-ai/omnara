import { describe, expect, it } from 'vitest'

import {
  configuredModelFormDefaults,
  discoveredModelPrefill,
  providerChangeReset,
} from './CreateConfiguredModelDialogState'

const discoveredModel = {
  slug: 'nvidia/nemotron-3.5-light',
  context_window_tokens: 262_144,
  max_output_tokens: 16_384,
}

describe('discoveredModelPrefill', () => {
  it('uses the provider model slug as the generated name', () => {
    expect(discoveredModelPrefill(configuredModelFormDefaults, discoveredModel)).toContainEqual([
      'name',
      discoveredModel.slug,
    ])
  })

  it('updates a generated name and preserves a custom name', () => {
    const generatedValues = {
      ...configuredModelFormDefaults,
      name: 'old-model',
      providerModelSlug: 'old-model',
    }
    expect(discoveredModelPrefill(generatedValues, discoveredModel)).toContainEqual([
      'name',
      discoveredModel.slug,
    ])

    const customValues = { ...generatedValues, name: 'My model' }
    expect(discoveredModelPrefill(customValues, discoveredModel)).not.toContainEqual([
      'name',
      discoveredModel.slug,
    ])
  })
})

describe('providerChangeReset', () => {
  it('clears a generated name and preserves a custom name', () => {
    const generatedValues = {
      ...configuredModelFormDefaults,
      name: 'old-model',
      providerModelSlug: 'old-model',
    }
    expect(providerChangeReset(generatedValues)).toContainEqual(['name', ''])
    expect(providerChangeReset({ ...generatedValues, name: 'My model' })).not.toContainEqual([
      'name',
      '',
    ])
  })
})
