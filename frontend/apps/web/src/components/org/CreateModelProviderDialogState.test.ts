import { describe, expect, it } from 'vitest'

import { configuredModelRequestForDiscoveredModel } from './CreateModelProviderDialogState'

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
})
