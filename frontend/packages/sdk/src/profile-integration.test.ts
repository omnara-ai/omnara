import { describe, expect, it } from 'vitest'

import {
  chatIntegrationLauncher,
  githubIntegrationSettings,
  profileIntegrationDiscordKeyStatus,
  profileIntegrationProfiles,
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
      expect(created.settings).toStrictEqual({
        launcher: { profiles: [first, second] },
      })
      expect(chatIntegrationLauncher(created.settings)).toStrictEqual({ profiles: [first, second] })
      expect(profileIntegrationProfiles(created)).toEqual([first, second])
      expect(profileIntegrationProfileUpdate(created, [second, first])).toStrictEqual({
        settings: { launcher: { profiles: [second, first] } },
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
      }),
      settings: { launcher: { profiles: [first] }, other: 'preserved' },
    }
    expect(profileIntegrationProfileUpdate(current, [second])).toEqual({
      settings: { other: 'preserved', launcher: { profiles: [second] } },
    })
    expect(profileIntegrationProfileUpdate(current, [])).toEqual({
      settings: { other: 'preserved' },
    })
  })
  it.each(['slack_thread', 'discord_thread'] as const)(
    'accepts one to sixteen profiles for %s',
    (integrationKind) => {
      const profiles = Array.from(
        { length: 17 },
        (_, index) => `aprf_${String.fromCharCode(97 + index).repeat(26)}`,
      )
      for (const profileIds of [[first], profiles.slice(0, 16)]) {
        expect(
          profileIntegrationSetup({ integrationKind, name: 'support', profileIds }).settings,
        ).toStrictEqual({ launcher: { profiles: profileIds } })
      }
      for (const profileIds of [[], profiles]) {
        expect(() =>
          profileIntegrationSetup({ integrationKind, name: 'support', profileIds }),
        ).toThrow()
      }
      expect(
        profileIntegrationSetup({ integrationKind, name: 'support', launcher: false }).settings,
      ).toStrictEqual({})
    },
  )
  it('uses exactly one GitHub profile when launching is enabled', () => {
    const input = {
      integrationKind: 'github_pr' as const,
      name: 'reviews',
      profileId: first,
      repositoryId: '123',
    }
    expect(profileIntegrationSetup(input).settings).toEqual({
      launcher: { profile: first, trigger: 'both', repository_id: '123' },
    })
    expect(profileIntegrationSetup({ ...input, launcher: false }).settings).toEqual({})
    expect(() => profileIntegrationSetup({ ...input, profileIds: [first, second] })).toThrow(
      'exactly one profile',
    )
    expect(() => githubIntegrationSettings({ launcher: { profiles: [first, second] } })).toThrow()
    expect(() =>
      profileIntegrationSetup({ ...input, repositoryId: '9223372036854775808' }),
    ).toThrow()
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
