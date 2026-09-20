import { describe, expect, it } from 'vitest'

import {
  profileAppDiscordKeyStatus,
  profileAppLauncherScope,
  profileAppProfileUpdate,
  profileAppSetup,
} from './profile-app'

const first = `aprf_${'a'.repeat(26)}`
const second = `aprf_${'b'.repeat(26)}`
const third = `aprf_${'c'.repeat(26)}`
const base = { name: 'reviewer', profileId: first }

describe('app metadata setup', () => {
  it.each(['slack', 'github', 'discord'] as const)(
    'creates a %s draft without capability or credential defaults',
    (provider) => {
      expect(profileAppSetup({ name: 'engineering-2', provider, launcher: false })).toEqual({
        name: 'engineering-2',
        definition_id: `omnara.${provider}`,
        settings: {},
      })
    },
  )
  it.each(['', '1app', 'with space', 'under_score', 'a'.repeat(33), '\u200b'])(
    'rejects invalid immutable name %s',
    (name) => {
      expect(() => profileAppSetup({ name, provider: 'slack', launcher: false })).toThrow()
    },
  )
  it('keeps large repository IDs as decimal strings and saves only launcher settings', () => {
    expect(
      profileAppSetup({ ...base, provider: 'github', scopeRef: '9223372036854775807' }),
    ).toEqual({
      name: base.name,
      definition_id: 'omnara.github',
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
      expect(() => profileAppSetup({ ...base, provider: 'github', scopeRef })).toThrow()
    },
  )
  it.each(['slack', 'discord'] as const)(
    'offers multiple profiles through one %s app',
    (provider) => {
      const app = profileAppSetup({
        ...base,
        provider,
        profileIds: [first, second],
        scopeRef: provider === 'slack' ? 'T123' : '18446744073709551615',
      })
      expect(app.settings.launcher?.slots).toEqual([
        { key: 'default', agent_profile_id: first },
        { key: 'profile_2', agent_profile_id: second },
      ])
      expect(Object.keys(app.settings)).toEqual(['launcher'])
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
        profileAppSetup({ ...base, provider: 'slack', scopeRef: 'T123', profileIds }),
      ).toThrow()
    }
    expect(
      profileAppSetup({
        ...base,
        provider: 'slack',
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
      profileAppSetup({ ...base, provider: 'slack', scopeKind: 'channel', scopeRef: 'C123' })
        .settings.launcher,
    ).toMatchObject({ scope_kind: 'channel', scope_ref: 'C123', trigger: 'mention' })
    expect(
      profileAppSetup({ ...base, provider: 'github', scopeRef: '123', trigger: 'mention' }).settings
        .launcher?.trigger,
    ).toBe('mention')
    expect(() =>
      profileAppSetup({ ...base, provider: 'github', scopeRef: '123', scopeKind: 'channel' }),
    ).toThrow(/scope/)
    expect(() =>
      profileAppSetup({
        ...base,
        provider: 'discord',
        scopeRef: '123',
        trigger: 'pull_request_opened',
      }),
    ).toThrow(/trigger/)
    expect(() =>
      profileAppSetup({ ...base, provider: 'slack', scopeRef: 'T123', scopeKind: 'channel' }),
    ).toThrow(/channel ID/)
  })
})

describe('guided launcher scope editing', () => {
  it('validates guided scope edits without requiring profile IDs', () => {
    expect(
      profileAppLauncherScope({ provider: 'slack', scopeKind: 'channel', scopeRef: ' C123 ' }),
    ).toEqual({
      scope_kind: 'channel',
      scope_ref: 'C123',
      trigger: 'mention',
    })
    expect(() =>
      profileAppLauncherScope({ provider: 'discord', scopeRef: '18446744073709551616' }),
    ).toThrow(/too large/)
    expect(() =>
      profileAppLauncherScope({ provider: 'slack', scopeRef: 'T123', trigger: 'typo' }),
    ).toThrow(/trigger/)
    expect(() =>
      profileAppLauncherScope({ provider: 'github', scopeKind: 'channel', scopeRef: '123' }),
    ).toThrow(/scope/)
  })
})

describe('profile list editing', () => {
  const app = {
    name: 'shared-bot',
    definition_id: 'omnara.slack',
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
    const before = structuredClone(app)
    const updated = profileAppProfileUpdate(app, [third, first])
    expect(updated).toEqual({
      ...app,
      settings: {
        launcher: {
          ...app.settings.launcher,
          slots: [
            app.settings.launcher.slots[0],
            app.settings.launcher.slots[2],
            { key: 'profile_2', agent_profile_id: third },
          ],
        },
      },
    })
    expect(app).toEqual(before)
    expect(profileAppProfileUpdate(updated, [first, third])).toEqual(updated)
    expect(profileAppProfileUpdate(app, []).settings.launcher?.slots).toEqual([
      app.settings.launcher.slots[2],
    ])
  })
  it('preserves repeated profile slots and counts existing agents in the limit', () => {
    const repeated = {
      ...app,
      settings: {
        launcher: {
          ...app.settings.launcher,
          slots: [
            { key: 'a', agent_profile_id: first },
            { key: 'b', agent_profile_id: first },
          ],
        },
      },
    }
    expect(profileAppProfileUpdate(repeated, [first])).toEqual(repeated)
    expect(() => profileAppProfileUpdate(repeated, [])).toThrow(/at least one/)
    const full = {
      ...app,
      settings: {
        launcher: {
          ...app.settings.launcher,
          slots: Array.from({ length: 16 }, (_, i) => ({
            key: `agent_${i}`,
            agent_id: `agt_${'a'.repeat(26)}`,
          })),
        },
      },
    }
    expect(() => profileAppProfileUpdate(full, [first])).toThrow(/16 slots/)
    expect(profileAppProfileUpdate(full, []).settings.launcher?.slots).toHaveLength(16)
    expect(() =>
      profileAppProfileUpdate({ ...app, definition_id: 'omnara.github' }, [first]),
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
      profileAppDiscordKeyStatus({ definition: 'omnara.discord', slots, interactions: false }),
    ).toMatchObject({ required: true, missing: true })
  })
  it('supports independent handler configuration and validates exactly 64 hex characters', () => {
    const input = { definition: 'omnara.discord', slots: [], interactions: true }
    for (const public_key of ['ab'.repeat(32), 'AB'.repeat(32)])
      expect(profileAppDiscordKeyStatus({ ...input, providerConfig: { public_key } }).missing).toBe(
        false,
      )
    for (const public_key of [
      null,
      {},
      123,
      '',
      'ab'.repeat(31),
      'gg'.repeat(32),
      ` ${'ab'.repeat(32)}`,
    ])
      expect(profileAppDiscordKeyStatus({ ...input, providerConfig: { public_key } }).missing).toBe(
        true,
      )
    expect(profileAppDiscordKeyStatus({ ...input, definition: 'omnara.slack' }).required).toBe(
      false,
    )
    expect(profileAppDiscordKeyStatus({ ...input, interactions: false }).required).toBe(false)
  })
})
