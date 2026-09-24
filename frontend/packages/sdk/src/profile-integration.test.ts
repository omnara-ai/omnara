import { describe, expect, it } from 'vitest'

import {
  profileIntegrationDiscordKeyStatus,
  profileIntegrationLauncherScope,
  profileIntegrationProfileUpdate,
  profileIntegrationSetup,
} from './profile-integration'

const first = `aprf_${'a'.repeat(26)}`
const second = `aprf_${'b'.repeat(26)}`
const third = `aprf_${'c'.repeat(26)}`
const base = { name: 'reviewer', profileId: first }

describe('integration metadata setup', () => {
  it.each(['slack_thread', 'github_pr', 'discord_thread'] as const)(
    'creates a %s draft without capability or credential defaults',
    (integrationType) => {
      expect(
        profileIntegrationSetup({ name: 'engineering-2', integrationType, launcher: false }),
      ).toEqual({
        name: 'engineering-2',
        integration_type: integrationType,
        settings: {},
      })
    },
  )
  it.each(['', '1app', 'with space', 'under_score', 'a'.repeat(33), '\u200b'])(
    'rejects invalid immutable name %s',
    (name) => {
      expect(() =>
        profileIntegrationSetup({ name, integrationType: 'slack_thread', launcher: false }),
      ).toThrow()
    },
  )
  it('defaults new GitHub launchers to installation scope and keeps explicit repository scope', () => {
    expect(
      profileIntegrationSetup({ ...base, integrationType: 'github_pr', scopeRef: '222' }).settings
        .launcher,
    ).toMatchObject({
      scope_kind: 'installation',
      scope_ref: '222',
      trigger: 'pull_request_opened',
    })
    expect(
      profileIntegrationSetup({
        ...base,
        integrationType: 'github_pr',
        scopeKind: 'repository',
        scopeRef: '333',
      }).settings.launcher,
    ).toMatchObject({ scope_kind: 'repository', scope_ref: '333' })
  })
  it('keeps large repository IDs as decimal strings and saves only launcher settings', () => {
    expect(
      profileIntegrationSetup({
        ...base,
        integrationType: 'github_pr',
        scopeKind: 'repository',
        scopeRef: '9223372036854775807',
      }),
    ).toEqual({
      name: base.name,
      integration_type: 'github_pr',
      settings: {
        launcher: {
          trigger: 'pull_request_opened',
          scope_kind: 'repository',
          scope_ref: '9223372036854775807',
          slots: [{ key: 'default', agent_profile_id: first }],
        },
      },
    })
  })
  it.each(['owner/repo', '01', '0', '9223372036854775808'])(
    'rejects invalid repository ID %s',
    (scopeRef) => {
      expect(() =>
        profileIntegrationSetup({
          ...base,
          integrationType: 'github_pr',
          scopeKind: 'repository',
          scopeRef,
        }),
      ).toThrow()
    },
  )
  it.each(['slack_thread', 'discord_thread'] as const)(
    'offers multiple profiles through one %s integration',
    (integrationType) => {
      const integration = profileIntegrationSetup({
        ...base,
        integrationType,
        profileIds: [first, second],
        scopeRef: integrationType === 'slack_thread' ? 'T123' : undefined,
      })
      expect(integration.settings.launcher?.slots).toEqual([
        { key: 'default', agent_profile_id: first },
        { key: 'profile_2', agent_profile_id: second },
      ])
      expect(Object.keys(integration.settings)).toEqual(['launcher'])
    },
  )
  it('bounds profiles and rejects duplicates, empty selections and invalid IDs', () => {
    for (const profileIds of [
      [],
      [first, first],
      ['invalid'],
      Array.from({ length: 17 }, (_, i) => `aprf_${String.fromCharCode(97 + i).repeat(26)}`),
    ]) {
      expect(() =>
        profileIntegrationSetup({
          ...base,
          integrationType: 'slack_thread',
          scopeRef: 'T123',
          profileIds,
        }),
      ).toThrow()
    }
    expect(
      profileIntegrationSetup({
        ...base,
        integrationType: 'slack_thread',
        scopeRef: 'T123',
        profileIds: Array.from(
          { length: 16 },
          (_, i) => `aprf_${String.fromCharCode(97 + i).repeat(26)}`,
        ),
      }).settings.launcher?.slots,
    ).toHaveLength(16)
  })
  it('supports Slack channels and GitHub mentions', () => {
    expect(
      profileIntegrationSetup({
        ...base,
        integrationType: 'slack_thread',
        scopeKind: 'channel',
        scopeRef: 'C123',
      }).settings.launcher,
    ).toMatchObject({ scope_kind: 'channel', scope_ref: 'C123', trigger: 'mention' })
    expect(
      profileIntegrationSetup({
        ...base,
        integrationType: 'github_pr',
        scopeRef: '123',
        trigger: 'mention',
      }).settings.launcher?.trigger,
    ).toBe('mention')
    expect(() =>
      profileIntegrationSetup({
        ...base,
        integrationType: 'github_pr',
        scopeRef: '123',
        scopeKind: 'channel',
      }),
    ).toThrow(/scope/)
    expect(() =>
      profileIntegrationSetup({
        ...base,
        integrationType: 'discord_thread',
        scopeRef: '123',
        trigger: 'pull_request_opened',
      }),
    ).toThrow(/trigger/)
    expect(() =>
      profileIntegrationSetup({
        ...base,
        integrationType: 'slack_thread',
        scopeRef: 'T123',
        scopeKind: 'channel',
      }),
    ).toThrow(/channel ID/)
  })
  it('launches Discord mentions without a server filter', () => {
    expect(
      profileIntegrationSetup({ ...base, integrationType: 'discord_thread' }).settings.launcher,
    ).toEqual({
      trigger: 'mention',
      slots: [{ key: 'default', agent_profile_id: first }],
    })
    expect(() =>
      profileIntegrationSetup({
        ...base,
        integrationType: 'discord_thread',
        scopeKind: 'channel',
        scopeRef: '123',
      }),
    ).toThrow(/scope/)
  })
})

describe('guided launcher scope editing', () => {
  it('validates guided scope edits without requiring profile IDs', () => {
    expect(
      profileIntegrationLauncherScope({
        integrationType: 'slack_thread',
        scopeKind: 'channel',
        scopeRef: ' C123 ',
      }),
    ).toEqual({
      scope_kind: 'channel',
      scope_ref: 'C123',
      trigger: 'mention',
    })
    expect(() =>
      profileIntegrationLauncherScope({
        integrationType: 'github_pr',
        scopeRef: '9223372036854775808',
      }),
    ).toThrow(/too large/)
    expect(() =>
      profileIntegrationLauncherScope({
        integrationType: 'slack_thread',
        scopeRef: 'T123',
        trigger: 'typo',
      }),
    ).toThrow(/trigger/)
    expect(() =>
      profileIntegrationLauncherScope({
        integrationType: 'github_pr',
        scopeKind: 'channel',
        scopeRef: '123',
      }),
    ).toThrow(/scope/)
  })
})

describe('profile list editing', () => {
  const integration = {
    name: 'shared-bot',
    integration_type: 'slack_thread' as const,
    settings: {
      launcher: {
        trigger: 'mention',
        scope_kind: 'workspace',
        scope_ref: 'T123',
        slots: [
          { key: 'keep', agent_profile_id: first },
          { key: 'profile_1', agent_profile_id: second },
          { key: 'existing', agent_id: `agt_${'a'.repeat(26)}` },
        ],
      },
    },
  }
  it('preserves immutable identity, slot names, order and fixed agents without mutating input', () => {
    const before = structuredClone(integration)
    const updated = profileIntegrationProfileUpdate(integration, [third, first])
    expect(updated).toEqual({
      ...integration,
      settings: {
        launcher: {
          ...integration.settings.launcher,
          slots: [
            integration.settings.launcher.slots[0],
            integration.settings.launcher.slots[2],
            { key: 'profile_2', agent_profile_id: third },
          ],
        },
      },
    })
    expect(integration).toEqual(before)
    expect(profileIntegrationProfileUpdate(updated, [first, third])).toEqual(updated)
    expect(profileIntegrationProfileUpdate(integration, []).settings.launcher?.slots).toEqual([
      integration.settings.launcher.slots[2],
    ])
  })
  it('preserves repeated profile slots and counts existing agents in the limit', () => {
    const repeated = {
      ...integration,
      settings: {
        launcher: {
          ...integration.settings.launcher,
          slots: [
            { key: 'a', agent_profile_id: first },
            { key: 'b', agent_profile_id: first },
          ],
        },
      },
    }
    expect(profileIntegrationProfileUpdate(repeated, [first])).toEqual(repeated)
    expect(() => profileIntegrationProfileUpdate(repeated, [])).toThrow(/at least one/)
    const full = {
      ...integration,
      settings: {
        launcher: {
          ...integration.settings.launcher,
          slots: Array.from({ length: 16 }, (_, i) => ({
            key: `agent_${i}`,
            agent_id: `agt_${'a'.repeat(26)}`,
          })),
        },
      },
    }
    expect(() => profileIntegrationProfileUpdate(full, [first])).toThrow(/16 slots/)
    expect(profileIntegrationProfileUpdate(full, []).settings.launcher?.slots).toHaveLength(16)
    expect(() =>
      profileIntegrationProfileUpdate({ ...integration, integration_type: 'github_pr' }, [first]),
    ).toThrow(/Slack or Discord/)
  })
})

describe('Discord interaction key', () => {
  it.each([
    [{ agent_profile_id: first }],
    [{ agent_id: `agt_${'a'.repeat(26)}` }],
    [{ agent_profile_id: first }, { agent_profile_id: second }],
  ])('requires a public key for every Discord launcher', (...slots) => {
    expect(
      profileIntegrationDiscordKeyStatus({
        integrationType: 'discord_thread',
        slots,
        interactions: false,
      }),
    ).toMatchObject({ required: true, missing: true })
  })
  it('supports independent handler configuration and validates exactly 64 hex characters', () => {
    const input = { integrationType: 'discord_thread' as const, slots: [], interactions: true }
    for (const public_key of ['ab'.repeat(32), 'AB'.repeat(32)])
      expect(
        profileIntegrationDiscordKeyStatus({ ...input, providerConfig: { public_key } }).missing,
      ).toBe(false)
    for (const public_key of [
      null,
      {},
      123,
      '',
      'ab'.repeat(31),
      'gg'.repeat(32),
      ` ${'ab'.repeat(32)}`,
    ])
      expect(
        profileIntegrationDiscordKeyStatus({ ...input, providerConfig: { public_key } }).missing,
      ).toBe(true)
    expect(
      profileIntegrationDiscordKeyStatus({ ...input, integrationType: 'slack_thread' }).required,
    ).toBe(false)
    expect(profileIntegrationDiscordKeyStatus({ ...input, interactions: false }).required).toBe(
      false,
    )
  })
})
