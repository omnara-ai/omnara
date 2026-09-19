import { describe, expect, it } from 'vitest'

import { profileAppDiscordKeyStatus, profileAppProfileUpdate, profileAppSetup } from './profile-app'

const base = {
  name: 'Reviewer',
  connectionId: `iin_${'a'.repeat(26)}`,
  profileId: `aprf_${'a'.repeat(26)}`,
  listen: true,
  interactions: false,
}

describe('Discord public key requirements', () => {
  const profileSlot = { agent_profile_id: base.profileId }
  const fixedSlot = { agent_id: `agt_${'a'.repeat(26)}` }
  it.each([
    {
      label: 'fixed agents only',
      slots: [fixedSlot, fixedSlot],
      interactions: false,
      required: false,
    },
    {
      label: 'one profile and a fixed agent',
      slots: [profileSlot, fixedSlot],
      interactions: false,
      required: false,
    },
    {
      label: 'duplicate profile options',
      slots: [profileSlot, profileSlot, fixedSlot],
      interactions: false,
      required: true,
    },
    {
      label: 'distinct profiles',
      slots: [profileSlot, { agent_profile_id: `aprf_${'b'.repeat(26)}` }],
      interactions: false,
      required: true,
    },
    {
      label: 'one profile with an interaction handler',
      slots: [profileSlot, fixedSlot],
      interactions: true,
      required: true,
    },
    {
      label: 'interaction handler without a launcher',
      slots: [],
      interactions: true,
      required: true,
    },
  ])('$label', ({ slots, interactions, required }) => {
    expect(
      profileAppDiscordKeyStatus({ definition: 'omnara.discord', slots, interactions }),
    ).toMatchObject({ required, missing: required })
  })

  it('accepts exactly 64 hexadecimal characters without coercing or trimming saved values', () => {
    const input = {
      definition: 'omnara.discord',
      slots: [profileSlot, profileSlot],
      interactions: false,
    }
    for (const public_key of ['ab'.repeat(32), 'AB'.repeat(32)]) {
      expect(
        profileAppDiscordKeyStatus({ ...input, providerConfig: { public_key } }),
      ).toMatchObject({ required: true, missing: false })
    }
    for (const public_key of [
      undefined,
      null,
      {},
      123,
      '',
      'ab'.repeat(31),
      'ab'.repeat(33),
      'gg'.repeat(32),
      ` ${'ab'.repeat(32)}`,
    ]) {
      expect(
        profileAppDiscordKeyStatus({ ...input, providerConfig: { public_key } }),
      ).toMatchObject({ required: true, missing: true })
    }
    expect(profileAppDiscordKeyStatus(input).pattern).toBe('[a-fA-F0-9]{64}')
  })

  it.each(['omnara.slack', 'omnara.github', undefined])(
    'does not require Discord credentials for %s',
    (definition) => {
      expect(
        profileAppDiscordKeyStatus({
          definition,
          slots: [profileSlot, profileSlot],
          interactions: true,
        }),
      ).toMatchObject({ required: false, missing: false })
    },
  )
})

describe('profile app setup', () => {
  it('keeps large repository identities as decimal strings and freezes only selected capabilities', () => {
    const app = profileAppSetup({
      ...base,
      provider: 'github',
      scopeRef: '9223372036854775807',
      tools: ['github_read'],
    })
    expect(app.settings.launcher).toEqual({
      trigger: 'pull_request_opened',
      scope_kind: 'repository',
      scope_ref: '9223372036854775807',
      slots: [{ key: 'default', agent_profile_id: base.profileId }],
    })
    expect(app.settings.resource.tools).toEqual({ github_read: {} })
    expect(app.settings.resource.scope).toBeUndefined()
    expect(app.settings.resource.interaction_handler).toBeUndefined()
  })
  it('makes Discord listening, tools, and interaction handling independently selectable', () => {
    const app = profileAppSetup({
      ...base,
      provider: 'discord',
      scopeRef: '18446744073709551615',
      tools: [],
      listen: false,
      interactions: true,
    })
    expect(app.settings.resource.tools).toEqual({})
    expect(app.settings.resource.listener).toBeUndefined()
    expect(app.settings.resource.interaction_handler).toEqual({
      definition: 'omnara.discord.interactions',
    })
  })
  it.each(['owner/repo', '01', '0', '9223372036854775808'])(
    'rejects invalid repository identity %s',
    (scopeRef) => {
      expect(() => profileAppSetup({ ...base, provider: 'github', scopeRef, tools: [] })).toThrow()
    },
  )
  it('rejects tools from another provider and GitHub interaction handlers', () => {
    expect(() =>
      profileAppSetup({
        ...base,
        provider: 'github',
        scopeRef: '123',
        tools: ['slack_post_message'],
      }),
    ).toThrow(/tool/)
    expect(() =>
      profileAppSetup({
        ...base,
        provider: 'github',
        scopeRef: '123',
        tools: [],
        interactions: true,
      }),
    ).toThrow(/interaction/)
  })
  it.each(['slack', 'discord'] as const)(
    'offers multiple profiles through the same %s connection',
    (provider) => {
      const ids = [base.profileId, `aprf_${'b'.repeat(26)}`]
      const app = profileAppSetup({
        ...base,
        provider,
        profileIds: ids,
        scopeRef: provider === 'slack' ? 'T123' : '123',
        tools: [],
      })
      expect(app.settings.launcher?.slots).toEqual([
        { key: 'default', agent_profile_id: ids[0] },
        { key: 'profile_2', agent_profile_id: ids[1] },
      ])
      expect(app.settings.resource.connection).toBe(base.connectionId)
    },
  )
  it('bounds the list and rejects empty, duplicate, or invalid profile IDs', () => {
    for (const ids of [
      [],
      [base.profileId, base.profileId],
      ['invalid'],
      Array.from({ length: 17 }, (_, i) => `aprf_${String.fromCharCode(97 + i).repeat(26)}`),
    ]) {
      expect(() =>
        profileAppSetup({
          ...base,
          provider: 'slack',
          profileIds: ids,
          scopeRef: 'T123',
          tools: [],
        }),
      ).toThrow()
    }
    const ids = Array.from(
      { length: 16 },
      (_, i) => `aprf_${String.fromCharCode(97 + i).repeat(26)}`,
    )
    expect(
      profileAppSetup({
        ...base,
        provider: 'slack',
        profileId: undefined,
        profileIds: ids,
        scopeRef: 'T123',
        tools: [],
      }).settings.launcher?.slots,
    ).toHaveLength(16)
  })
})

describe('profile list editing', () => {
  const first = base.profileId,
    second = `aprf_${'b'.repeat(26)}`,
    third = `aprf_${'c'.repeat(26)}`
  const app = {
    name: 'Shared bot',
    enabled: false,
    settings: {
      resource: {
        definition: 'omnara.slack',
        connection: base.connectionId,
        config: { arbitrary: { nested: ['preserve'] } },
        tools: { slack_read: { permission: { mode: 'always_allow' } } },
        scope: { slack: { channel_id: 'C123' } },
        listener: { events: ['message'], config: { custom: true } },
        interaction_handler: {
          definition: 'omnara.slack.interactions',
          config: { custom: 'value' },
        },
      },
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
  it('preserves keys, order, mixed slots and all unrelated settings without mutating input', () => {
    const before = structuredClone(app)
    const result = profileAppProfileUpdate(app, [third, first])
    expect(result).toEqual({
      ...app,
      settings: {
        ...app.settings,
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
    expect(profileAppProfileUpdate(result, [first, third])).toEqual(result)
  })
  it('can remove all profiles from a mixed setup while preserving existing agents', () => {
    expect(profileAppProfileUpdate(app, []).settings.launcher?.slots).toEqual([
      app.settings.launcher.slots[2],
    ])
  })
  it('preserves repeated saved profile slots rather than silently collapsing a generic setup', () => {
    const repeated = {
      ...app,
      settings: {
        ...app.settings,
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
  })
  it('counts existing slots in the bound and forbids empty launchers and edits to GitHub', () => {
    const full = {
      ...app,
      settings: {
        ...app.settings,
        launcher: {
          ...app.settings.launcher,
          slots: Array.from({ length: 16 }, (_, i) => ({
            key: `slot_${i}`,
            agent_id: `agt_${'a'.repeat(26)}`,
          })),
        },
      },
    }
    expect(() => profileAppProfileUpdate(full, [first])).toThrow(/16 slots/)
    expect(profileAppProfileUpdate(full, []).settings.launcher?.slots).toHaveLength(16)
    const single = profileAppSetup({ ...base, provider: 'slack', scopeRef: 'T123', tools: [] })
    expect(() => profileAppProfileUpdate(single, [])).toThrow(/at least one/)
    expect(() =>
      profileAppProfileUpdate(
        {
          ...app,
          settings: {
            ...app.settings,
            resource: { ...app.settings.resource, definition: 'omnara.github' },
          },
        },
        [first],
      ),
    ).toThrow(/Slack or Discord/)
  })
})

describe('project-first setup options', () => {
  it.each(['slack', 'github', 'discord'] as const)(
    'allows reusable %s defaults without profiles or scope',
    (provider) => {
      const app = profileAppSetup({
        ...base,
        provider,
        launcher: false,
        profileId: undefined,
        tools: [],
        listen: false,
      })
      expect(app.settings).toEqual({
        resource: { definition: `omnara.${provider}`, connection: base.connectionId, tools: {} },
      })
    },
  )
  it('supports GitHub mentions without changing the CLI PR-open default', () => {
    const input = { ...base, provider: 'github' as const, scopeRef: '123', tools: [] }
    expect(profileAppSetup(input).settings.launcher?.trigger).toBe('pull_request_opened')
    expect(profileAppSetup({ ...input, trigger: 'mention' }).settings.launcher?.trigger).toBe(
      'mention',
    )
  })
  it.each(['C123', 'G123'])(
    'supports Slack channel %s while retaining workspace defaults',
    (scopeRef) => {
      expect(
        profileAppSetup({ ...base, provider: 'slack', tools: [], scopeRef, scopeKind: 'channel' })
          .settings.launcher,
      ).toMatchObject({ trigger: 'mention', scope_kind: 'channel', scope_ref: scopeRef })
      expect(
        profileAppSetup({ ...base, provider: 'slack', tools: [], scopeRef: 'T123' }).settings
          .launcher?.scope_kind,
      ).toBe('workspace')
    },
  )
  it('rejects provider-mismatched scopes and triggers', () => {
    expect(() =>
      profileAppSetup({
        ...base,
        provider: 'discord',
        tools: [],
        scopeRef: '123',
        trigger: 'pull_request_opened',
      }),
    ).toThrow(/trigger/)
    expect(() =>
      profileAppSetup({
        ...base,
        provider: 'github',
        tools: [],
        scopeRef: '123',
        scopeKind: 'channel',
      }),
    ).toThrow(/scope/)
    expect(() =>
      profileAppSetup({
        ...base,
        provider: 'slack',
        tools: [],
        scopeRef: 'T123',
        scopeKind: 'channel',
      }),
    ).toThrow(/channel ID/)
  })
})

it('validates setup before credentials exist without inventing a connection ID', () => {
  const request = profileAppSetup({
    ...base,
    provider: 'github',
    connectionId: undefined,
    tools: ['github_read'],
    scopeRef: '123',
  })
  expect(request.settings.resource.connection).toBeUndefined()
  expect(request.settings.launcher?.slots).toEqual([
    { key: 'default', agent_profile_id: base.profileId },
  ])
  expect(() =>
    profileAppSetup({
      ...base,
      provider: 'github',
      connectionId: undefined,
      tools: [],
      scopeRef: '9223372036854775808',
    }),
  ).toThrow(/scope ID/)
})
