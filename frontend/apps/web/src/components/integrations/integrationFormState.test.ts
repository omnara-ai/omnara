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
      settings: { launcher: { profiles: [first, second], channel_id: 'C123' }, other: 'kept' },
    })
    const values = integrationFormValues('slack_thread', saved)
    expect(
      integrationFormRequest('slack_thread', { ...values, profileIds: [second, first] }, saved),
    ).toEqual({
      settings: { other: 'kept', launcher: { profiles: [second, first], channel_id: 'C123' } },
    })
    expect(integrationFormRequest('slack_thread', { ...values, profileIds: [] }, saved)).toEqual({
      settings: { other: 'kept' },
    })
  })
  it('uses implicit account scope and an optional channel', () => {
    const saved = integrationFixture({ settings: { launcher: { profiles: [first] } } })
    const values = integrationFormValues('slack_thread', saved)
    expect(values.scopeRef).toBe('')
    expect(values.scopeKind).toBe('')
    expect(integrationFormRequest('slack_thread', values, saved).settings.launcher).toEqual({
      profiles: [first],
      channel_id: undefined,
    })
    expect(
      validateIntegrationForm(
        'slack_thread',
        { ...values, scopeKind: 'channel', scopeRef: '' },
        saved,
      ).error,
    ).toBe('Enter a scope ID.')
  })
  it('preserves GitHub sender policy while launch is disabled and preserves repository when enabled', () => {
    const saved = integrationFixture({
      integration_kind: 'github_pr',
      settings: {
        sender_policy: 'anyone',
        launcher: { profile: first, trigger: 'mention', repository_id: '123' },
      },
    })
    const values = integrationFormValues('github_pr', saved)
    expect(integrationFormRequest('github_pr', { ...values, launcher: false }, saved)).toEqual({
      settings: { sender_policy: 'anyone' },
    })
    expect(
      integrationFormRequest(
        'github_pr',
        { ...values, profileIds: [second], trigger: 'pull_request_opened' },
        saved,
      ),
    ).toEqual({
      settings: {
        sender_policy: 'anyone',
        launcher: { profile: second, trigger: 'pull_request_opened', repository_id: '123' },
      },
    })
  })
  it('defaults comment policy to writers and validates single-profile selection', () => {
    const saved = integrationFixture({ integration_kind: 'github_pr' })
    const values = integrationFormValues('github_pr', saved)
    expect(values.senderPolicy).toBe('writers')
    expect(integrationFormRequest('github_pr', values, saved)).toEqual({
      settings: { sender_policy: 'writers' },
    })
    expect(() =>
      integrationFormRequest(
        'github_pr',
        { ...values, launcher: true, profileIds: [first, second] },
        saved,
      ),
    ).toThrow('exactly one profile')
  })
})
