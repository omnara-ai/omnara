import type { DiscoveredProviderModel } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import {
  canCreateDiscoveredModel,
  configuredModelRequestForDiscoveredModel,
} from './CreateModelProviderDialogState'

describe('configuredModelRequestForDiscoveredModel', () => {
  it('uses the discovered provider model slug as the configured model name', () => {
    expect(
      configuredModelRequestForDiscoveredModel(
        {
          slug: 'nvidia/nemotron-3.5-light',
          display_name: 'NVIDIA Nemotron 3.5 Light',
          context_window_tokens: 262_144,
          max_output_tokens: 16_384,
        },
        'openai-chat-completions',
      ),
    ).toEqual({
      name: 'nvidia/nemotron-3.5-light',
      provider_model_slug: 'nvidia/nemotron-3.5-light',
      context_window_tokens: 262_144,
      max_output_tokens: 16_384,
      supports_tools: true,
      supports_reasoning: false,
    })
  })

  it.each(['anthropic-messages', 'openai-chat-completions', 'openai-responses'] as const)(
    'filters unusable bulk capacity for %s without setting a default allowance',
    (apiFormat) => {
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
      const creatable = models.filter((model) => canCreateDiscoveredModel(model, apiFormat))
      expect(creatable).toEqual(apiFormat === 'anthropic-messages' ? [known] : [known, unknown])
      for (const model of creatable) {
        const request = configuredModelRequestForDiscoveredModel(model, apiFormat)
        expect(request.default_max_output_tokens).toBeUndefined()
        expect(request.max_output_tokens).toBe(model.max_output_tokens)
      }
      if (apiFormat === 'anthropic-messages') {
        expect(() => configuredModelRequestForDiscoveredModel(unknown, apiFormat)).toThrow(
          'Token limits are missing or invalid',
        )
      }
    },
  )
})
