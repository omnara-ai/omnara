import { describe, expect, it } from 'vitest'

import { fakeId, integration as integrationFixture } from '@/test/fixtures'

import {
  integrationFormRequest,
  integrationFormValues,
  validateIntegrationForm,
} from './integrationFormState'

const first = fakeId('aprf'),
  second = `aprf_${'b'.repeat(26)}`
describe('integration settings editor', () => {
  it('sends only settings and preserves unrelated settings when editing profiles', () => {
    const saved = integrationFixture({
      settings: { launcher: { profiles: [first, second] }, other: 'kept' },
    })
    const values = integrationFormValues('slack_thread', saved)
    expect(
      integrationFormRequest('slack_thread', { ...values, profileIds: [second, first] }, saved),
    ).toEqual({
      settings: { other: 'kept', launcher: { profiles: [second, first] } },
    })
    expect(integrationFormRequest('slack_thread', { ...values, profileIds: [] }, saved)).toEqual({
      settings: { other: 'kept' },
    })
  })
  it.each(['slack_thread', 'discord_thread'] as const)(
    'keeps %s mention profiles independent from schedule destinations',
    (integrationKind) => {
      const saved = integrationFixture({
        integration_kind: integrationKind,
        provider_config: { public_key: 'ab'.repeat(32) },
        settings: { launcher: { profiles: [first, second] } },
      })
      const values = integrationFormValues(integrationKind, saved)
      expect(values.repositoryId).toBe('')
      expect(validateIntegrationForm(integrationKind, values, saved)).toBe('')
      expect(integrationFormRequest(integrationKind, values, saved)).toEqual({
        settings: { launcher: { profiles: [first, second] } },
      })
      expect(integrationFormRequest(integrationKind, { ...values, profileIds: [] }, saved)).toEqual(
        {
          settings: {},
        },
      )
    },
  )
  it('preserves the GitHub repository when editing the launcher and removes disabled launchers', () => {
    const saved = integrationFixture({
      integration_kind: 'github_pr',
      settings: {
        launcher: { profile: first, trigger: 'mention', repository_id: '123' },
      },
    })
    const values = integrationFormValues('github_pr', saved)
    expect(values.repositoryId).toBe('123')
    expect(integrationFormRequest('github_pr', { ...values, launcher: false }, saved)).toEqual({
      settings: {},
    })
    expect(
      integrationFormRequest(
        'github_pr',
        { ...values, profileIds: [second], trigger: 'pull_request_opened' },
        saved,
      ),
    ).toEqual({
      settings: {
        launcher: { profile: second, trigger: 'pull_request_opened', repository_id: '123' },
      },
    })
  })
  it('allows a disabled GitHub launcher and validates single-profile selection', () => {
    const saved = integrationFixture({ integration_kind: 'github_pr' })
    const values = integrationFormValues('github_pr', saved)
    expect(values.launcher).toBe(false)
    expect(integrationFormRequest('github_pr', { ...values, launcher: false }, saved)).toEqual({
      settings: {},
    })
    const initial = integrationFormValues('github_pr', saved, true)
    expect(initial.launcher).toBe(true)
    expect(initial.trigger).toBe('both')
    expect(validateIntegrationForm('github_pr', initial, saved)).toBe(
      'Choose at least one profile.',
    )
    expect(integrationFormRequest('github_pr', { ...initial, profileIds: [first] }, saved)).toEqual(
      {
        settings: { launcher: { profile: first, trigger: 'both' } },
      },
    )
    expect(() =>
      integrationFormRequest(
        'github_pr',
        { ...values, launcher: true, profileIds: [first, second] },
        saved,
      ),
    ).toThrow('exactly one profile')
  })
})
