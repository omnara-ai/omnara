import { describe, expect, it } from 'vitest'

import {
  resourceNameError,
  resourceNameMaxCodePoints,
  resourceNameSuggestion,
  resourceNameValid,
} from './resource-name'

describe('resource names', () => {
  it.each(['Studio  54', '研究開発 شركة برمجيات', '🚀 Lab', 'R&D (West)', '.!?', 'Cafe\u0301'])(
    'accepts and preserves %s',
    (name) => {
      expect(resourceNameValid(name)).toBe(true)
    },
  )

  it('counts Unicode code points', () => {
    expect(resourceNameValid('😀'.repeat(resourceNameMaxCodePoints))).toBe(true)
    expect(resourceNameError('界'.repeat(resourceNameMaxCodePoints + 1))).toContain(
      `${resourceNameMaxCodePoints} Unicode characters`,
    )
  })

  it.each([
    ['leading space', ' Acme', 'start or end'],
    ['trailing space', 'Acme ', 'start or end'],
    ['non-ASCII space', 'Acme\u00a0Labs', 'ordinary spaces'],
    ['tab', 'Acme\tLabs', 'invisible, control, or format'],
    ['newline', 'Acme\nLabs', 'invisible, control, or format'],
    ['NUL', 'Acme\x00Labs', 'invisible, control, or format'],
    ['zero-width joiner', 'Acme\u200dLabs', 'invisible, control, or format'],
    ['bidi override', 'Acme\u202eLabs', 'invisible, control, or format'],
    ['Hangul filler', 'Acme\u3164Labs', 'invisible, control, or format'],
    ['variation selector', 'Acme\ufe0fLabs', 'invisible, control, or format'],
    ['braille blank', 'Acme\u2800Labs', 'invisible, control, or format'],
    ['unpaired surrogate', 'Acme\ud800Labs', 'invisible, control, or format'],
    ['replacement character', 'Acme\ufffdLabs', 'Unicode replacement character'],
  ])('rejects %s', (_case, name, message) => {
    expect(resourceNameError(name)).toContain(message)
  })

  it.each([
    '',
    'Studio 54',
    '研究開発 شركة برمجيات',
    '😀'.repeat(resourceNameMaxCodePoints),
    '界'.repeat(resourceNameMaxCodePoints + 1),
    ' Acme',
    'Acme ',
    'Acme\u00a0Labs',
    'Acme\tLabs',
    'Acme\u200dLabs',
    'Acme\u3164Labs',
    'Acme\ufe0fLabs',
    'Acme\u2800Labs',
    'Acme\ud800Labs',
    'Acme\ufffdLabs',
  ])('keeps field errors aligned with the OpenAPI schema for %j', (name) => {
    expect(resourceNameError(name) === undefined).toBe(resourceNameValid(name))
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
