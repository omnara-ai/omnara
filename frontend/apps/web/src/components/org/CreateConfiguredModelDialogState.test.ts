import { describe, expect, it } from 'vitest'

import {
  configuredModelDraft,
  configuredModelDraftError,
  configuredModelDraftRequest,
  configuredModelSuggestedName,
  configuredModelTokenLimitsError,
  discoveredModelMatches,
} from './CreateConfiguredModelDialogState'

const discovered = {
  slug: 'nvidia/nemotron-3.5-light',
  display_name: 'NVIDIA Nemotron 3.5 Light',
  context_window_tokens: 262_144,
  max_output_tokens: 16_384,
}

describe('configured model drafts', () => {
  it('names a discovered model after its slug and keeps its reported limits', () => {
    const draft = configuredModelDraft(discovered)
    expect(draft).toEqual({
      slug: discovered.slug,
      name: discovered.slug,
      contextWindowTokens: '262144',
      maxOutputTokens: '16384',
    })
    expect(configuredModelDraftError(draft)).toBe('')
    expect(configuredModelDraftRequest(draft)).toEqual({
      name: discovered.slug,
      provider_model_slug: discovered.slug,
      context_window_tokens: 262_144,
      max_output_tokens: 16_384,
      supports_tools: true,
      supports_reasoning: false,
    })
  })

  it('shortens names for slugs too long to be resource names', () => {
    const slug = `vendor/${'a'.repeat(80)}`
    const draft = configuredModelDraft({ slug })
    expect(draft.name).toBe(configuredModelSuggestedName(slug))
    expect(draft.name.length).toBeLessThanOrEqual(64)
  })

  it('requires a context window for models without reported limits', () => {
    const draft = configuredModelDraft({ slug: 'custom-model' })
    expect(configuredModelDraftError(draft)).toContain('Enter the context window')
    const completed = { ...draft, contextWindowTokens: '100000' }
    expect(configuredModelDraftError(completed)).toBe('')
    expect(configuredModelDraftRequest(completed)).not.toHaveProperty('max_output_tokens')
  })

  it('rejects an empty name and out-of-range token limits', () => {
    const draft = configuredModelDraft(discovered)
    expect(configuredModelDraftError({ ...draft, name: '' })).not.toBe('')
    expect(configuredModelDraftError({ ...draft, maxOutputTokens: '262144' })).toContain(
      'Max output must be',
    )
    expect(configuredModelDraftError({ ...draft, contextWindowTokens: '1' })).toContain(
      'Context window must be',
    )
  })
})

describe('configured model token limits', () => {
  const values = { contextWindowTokens: '100000', maxOutputTokens: '', defaultMaxOutputTokens: '' }

  it.each(['0', '-1', '1.5', '100000', '100001'])('rejects invalid capacity %s', (capacity) => {
    expect(configuredModelTokenLimitsError({ ...values, maxOutputTokens: capacity })).toContain(
      'Max output must be',
    )
  })

  it.each(['0', '-1', '1.5', '100000', '100001'])(
    'rejects invalid request allowance %s',
    (allowance) => {
      expect(
        configuredModelTokenLimitsError({ ...values, defaultMaxOutputTokens: allowance }),
      ).toContain('Default output must be')
    },
  )

  it('rejects a default allowance above max output', () => {
    expect(
      configuredModelTokenLimitsError({
        ...values,
        maxOutputTokens: '1000',
        defaultMaxOutputTokens: '2000',
      }),
    ).toContain('Default output must be')
  })
})

describe('discoveredModelMatches', () => {
  it('matches slugs and display names case-insensitively', () => {
    expect(discoveredModelMatches(discovered, 'NEMOTRON')).toBe(true)
    expect(discoveredModelMatches(discovered, 'light')).toBe(true)
    expect(discoveredModelMatches(discovered, 'qwen')).toBe(false)
    expect(discoveredModelMatches(discovered, '  ')).toBe(true)
  })
})
