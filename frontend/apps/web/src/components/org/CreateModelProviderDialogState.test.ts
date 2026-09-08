import type { DiscoveredProviderModel } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import {
  canCreateDiscoveredModel,
  configuredModelRequestForDiscoveredModel,
} from './CreateModelProviderDialogState'

describe('configuredModelRequestForDiscoveredModel', () => {
  it('uses the discovered provider model slug as the configured model name', () => {
    expect(
      configuredModelRequestForDiscoveredModel({
        slug: 'nvidia/nemotron-3.5-light',
        display_name: 'NVIDIA Nemotron 3.5 Light',
        context_window_tokens: 262_144,
        max_output_tokens: 16_384,
      }),
    ).toEqual({
      name: 'nvidia/nemotron-3.5-light',
      provider_model_slug: 'nvidia/nemotron-3.5-light',
      context_window_tokens: 262_144,
      max_output_tokens: 16_384,
      supports_tools: true,
      supports_reasoning: false,
    })
  })

  it('accepts unknown bulk capacity without setting a default allowance', () => {
    const known = { slug: 'known', context_window_tokens: 100000, max_output_tokens: 64000 }
    const unknown = { slug: 'unknown', context_window_tokens: 100000 }
    const models: DiscoveredProviderModel[] = [
      known,
      unknown,
      { slug: 'no-context', max_output_tokens: 64000 },
      { slug: 'fractional-context', context_window_tokens: 100000.5, max_output_tokens: 64000 },
      { ...known, slug: 'zero-output', max_output_tokens: 0 },
      { ...known, slug: 'fractional-output', max_output_tokens: 1.5 },
      { ...known, slug: 'no-input-space', max_output_tokens: 100000 },
    ]
    const creatable = models.filter(canCreateDiscoveredModel)
    expect(creatable).toEqual([known, unknown])
    for (const model of creatable) {
      const request = configuredModelRequestForDiscoveredModel(model)
      expect(request).not.toHaveProperty('default_max_output_tokens')
      expect(request.max_output_tokens).toBe(model.max_output_tokens)
    }
    expect(configuredModelRequestForDiscoveredModel(unknown)).not.toHaveProperty(
      'max_output_tokens',
    )
    expect(() => configuredModelRequestForDiscoveredModel({ slug: 'no-context' })).toThrow(
      'Token limits are missing or invalid',
    )
  })
})
