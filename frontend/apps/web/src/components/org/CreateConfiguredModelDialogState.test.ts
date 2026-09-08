import type { ModelProviderConfig } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import {
  configuredModelFormDefaults,
  configuredModelFormValid,
  configuredModelSuggestedName,
  configuredModelTokenLimitsError,
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

describe('configured model token limits', () => {
  const provider: ModelProviderConfig = {
    id: 'provider',
    org_id: 'org',
    management_kind: 'tenant',
    name: 'custom',
    api_format: 'openai-chat-completions',
    api_variant: 'default',
    base_url: 'https://api.example.com',
    endpoint_path: '/messages',
    request_timeout_ms: 120000,
    idle_timeout_ms: 300000,
    auth_kind: 'bearer_token',
    auth_options: {},
    credential_secret_id: 'secret',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  }
  const values = {
    ...configuredModelFormDefaults,
    name: 'custom',
    providerModelSlug: 'custom',
    contextWindowTokens: '100000',
  }

  it.each(['anthropic-messages', 'openai-chat-completions', 'openai-responses'] as const)(
    'accepts unknown capacity for %s',
    (apiFormat) => {
      const selectedProvider = { ...provider, api_format: apiFormat }
      expect(configuredModelFormValid(values, selectedProvider)).toBe(true)
      expect(
        configuredModelFormValid({ ...values, defaultMaxOutputTokens: '1000' }, selectedProvider),
      ).toBe(true)
      expect(
        configuredModelFormValid({ ...values, maxOutputTokens: '64000' }, selectedProvider),
      ).toBe(true)
    },
  )

  it.each(['', '1000', '20000'])(
    'preserves explicit default %s on discovery selection',
    (allowance) => {
      const before = { ...values, defaultMaxOutputTokens: allowance }
      const updates = discoveredModelPrefill(before, discoveredModel)
      expect(updates.filter(([field]) => field === 'defaultMaxOutputTokens')).toEqual([])
      const selected = { ...before, ...Object.fromEntries(updates) }
      expect(configuredModelFormValid(selected, provider)).toBe(allowance !== '20000')
      if (allowance === '20000') {
        expect(configuredModelTokenLimitsError(selected)).toContain('Default output must be')
      }
    },
  )

  it('accepts an unknown capacity and a separately chosen request allowance', () => {
    expect(configuredModelFormValid(values, provider)).toBe(true)
    expect(configuredModelFormValid({ ...values, defaultMaxOutputTokens: '64000' }, provider)).toBe(
      true,
    )
    expect(discoveredModelPrefill(values, { slug: 'custom' })).toContainEqual([
      'maxOutputTokens',
      '',
    ])
  })

  it.each(['0', '-1', '1.5', '100000', '100001'])(
    'rejects invalid request allowance %s even without a capacity',
    (allowance) => {
      expect(
        configuredModelFormValid({ ...values, defaultMaxOutputTokens: allowance }, provider),
      ).toBe(false)
    },
  )

  it.each(['0', '-1', '1.5', '100000', '100001'])('rejects invalid capacity %s', (capacity) => {
    expect(configuredModelTokenLimitsError({ ...values, maxOutputTokens: capacity })).toContain(
      'Max output must be',
    )
  })

  it('bounds explicit allowance by a known capacity and reserves input space', () => {
    expect(
      configuredModelFormValid(
        { ...values, maxOutputTokens: '64000', defaultMaxOutputTokens: '64000' },
        provider,
      ),
    ).toBe(true)
    expect(
      configuredModelFormValid(
        { ...values, maxOutputTokens: '64000', defaultMaxOutputTokens: '64001' },
        provider,
      ),
    ).toBe(false)
    expect(configuredModelFormValid({ ...values, contextWindowTokens: '1' }, provider)).toBe(false)
  })
})
