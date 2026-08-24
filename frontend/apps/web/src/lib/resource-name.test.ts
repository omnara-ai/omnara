import { describe, expect, it } from 'vitest'

import {
  resourceNameError,
  resourceNameMaxCodePoints,
  resourceNameSuggestion,
  resourceNameValid,
} from './resource-name'

describe('resourceNameError', () => {
  it('turns generated schema issues into field-specific messages', () => {
    expect(resourceNameError('')).toBe('Name is required.')
    expect(resourceNameError(' invalid', 'Provider name')).toBe(
      'Provider name must not start or end with whitespace.',
    )
    expect(resourceNameError('Valid name')).toBeUndefined()
  })
})

describe('resourceNameSuggestion', () => {
  it('prefers the first exact valid generated candidate', () => {
    expect(resourceNameSuggestion(['Provider - model', 'model'], 'Configured model')).toBe(
      'Provider - model',
    )
  })

  it('returns the validated fallback when no preferred candidate exists', () => {
    expect(resourceNameSuggestion([], 'Configured model')).toBe('Configured model')
  })

  it('uses a shorter exact candidate before truncating', () => {
    const combined = `${'Provider'.repeat(10)} - model-slug`
    expect(resourceNameSuggestion([combined, 'model-slug'], 'Configured model')).toBe('model-slug')
  })

  it('truncates a generated suggestion by Unicode code point when necessary', () => {
    const suggestion = resourceNameSuggestion(['😀'.repeat(70)], 'Configured model')
    expect(Array.from(suggestion)).toHaveLength(resourceNameMaxCodePoints)
    expect(resourceNameValid(suggestion)).toBe(true)
    expect(suggestion).toMatch(/-[a-f0-9]{16}$/)
  })

  it('truncates the later identity-focused candidate first', () => {
    const providerHeavy = `${'Provider '.repeat(10)}- ${'x'.repeat(80)}`
    const modelSlug = `model-identity-${'y'.repeat(80)}`
    const suggestion = resourceNameSuggestion([providerHeavy, modelSlug], 'Configured model')
    expect(suggestion).toMatch(/^model-identity-/)
    expect(suggestion).toMatch(/-[a-f0-9]{16}$/)
  })

  it('falls back instead of copying unsafe generated characters', () => {
    const suggestion = resourceNameSuggestion(['bad\tname'], 'Configured model')
    expect(suggestion).toMatch(/^Configured model-[a-f0-9]{16}$/)
    expect(resourceNameValid(suggestion)).toBe(true)
  })

  it('keeps distinct identities when long values share a truncated prefix', () => {
    const sharedPrefix = `accounts/fireworks/models/${'x'.repeat(60)}`
    const first = resourceNameSuggestion([`${sharedPrefix}-v1`], 'Configured model')
    const second = resourceNameSuggestion([`${sharedPrefix}-v2`], 'Configured model')
    expect(first).not.toBe(second)
  })
})
