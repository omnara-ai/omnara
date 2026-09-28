import { describe, expect, it } from 'vitest'

import {
  chatIntegrationLauncher,
  githubIntegrationSettings,
  profileIntegrationDiscordKeyStatus,
  profileIntegrationProfileUpdate,
  profileIntegrationSetup,
} from './profile-integration'

const first = `aprf_${'a'.repeat(26)}`
const second = `aprf_${'b'.repeat(26)}`

describe('integration-owned launcher settings', () => {
  it.each(['slack_thread', 'discord_thread'] as const)(
    'uses ordered distinct public profiles for %s',
    (integrationKind) => {
      const created = profileIntegrationSetup({
        integrationKind,
        name: 'support',
        profileIds: [first, second],
      })
      expect(created.settings).toEqual({
        launcher: { profiles: [first, second], channel_id: undefined },
      })
      expect(profileIntegrationProfileUpdate(created, [second, first])).toEqual({
        settings: { launcher: { profiles: [second, first], channel_id: undefined } },
      })
      expect(() =>
        profileIntegrationSetup({ integrationKind, name: 'support', profileIds: [first, first] }),
      ).toThrow('Choose each profile only once')
    },
  )
  it('removes a launcher while preserving other settings', () => {
    const current = {
      ...profileIntegrationSetup({
        integrationKind: 'slack_thread',
        name: 'support',
        profileId: first,
        channelId: 'C123',
      }),
      settings: { launcher: { profiles: [first], channel_id: 'C123' }, other: 'preserved' },
    }
    expect(profileIntegrationProfileUpdate(current, [second])).toEqual({
      settings: { other: 'preserved', launcher: { profiles: [second], channel_id: 'C123' } },
    })
    expect(profileIntegrationProfileUpdate(current, [])).toEqual({
      settings: { other: 'preserved' },
    })
  })
  it('limits profiles and validates channel restrictions', () => {
    expect(() =>
      profileIntegrationSetup({
        integrationKind: 'slack_thread',
        name: 'support',
        profileIds: Array.from({ length: 17 }, () => first),
      }),
    ).toThrow()
    expect(() =>
      profileIntegrationSetup({
        integrationKind: 'slack_thread',
        name: 'support',
        profileId: first,
        channelId: 'T123',
      }),
    ).toThrow()
    expect(() =>
      profileIntegrationSetup({
        integrationKind: 'discord_thread',
        name: 'support',
        profileId: first,
        channelId: '123',
      }),
    ).toThrow()
  })
  it('uses exactly one GitHub profile with independent comment policy', () => {
    const input = {
      integrationKind: 'github_pr' as const,
      name: 'reviews',
      profileId: first,
      repositoryId: '123',
    }
    expect(profileIntegrationSetup(input).settings).toEqual({
      sender_policy: 'writers',
      launcher: { profile: first, trigger: 'pull_request_opened', repository_id: '123' },
    })
    expect(
      profileIntegrationSetup({ ...input, launcher: false, senderPolicy: 'anyone' }).settings,
    ).toEqual({ sender_policy: 'anyone' })
    expect(() => profileIntegrationSetup({ ...input, profileIds: [first, second] })).toThrow(
      'exactly one profile',
    )
    expect(() =>
      profileIntegrationSetup({ ...input, repositoryId: '9223372036854775808' }),
    ).toThrow()
    expect(githubIntegrationSettings({}).sender_policy).toBe('writers')
  })
  it('rejects removed slots rather than quietly changing launch behavior', () => {
    expect(() =>
      chatIntegrationLauncher({
        launcher: { slots: [{ key: 'default', agent_profile_id: first }] },
      }),
    ).toThrow()
    expect(() => githubIntegrationSettings({ launcher: { profiles: [first, second] } })).toThrow()
  })
  it('requires a valid Discord key for a launcher or interactions', () => {
    const input = {
      integrationKind: 'discord_thread' as const,
      launcher: true,
      interactions: false,
    }
    expect(profileIntegrationDiscordKeyStatus(input).missing).toBe(true)
    expect(
      profileIntegrationDiscordKeyStatus({
        ...input,
        providerConfig: { public_key: 'ab'.repeat(32) },
      }).missing,
    ).toBe(false)
    expect(profileIntegrationDiscordKeyStatus({ ...input, launcher: false }).required).toBe(false)
  })
})
