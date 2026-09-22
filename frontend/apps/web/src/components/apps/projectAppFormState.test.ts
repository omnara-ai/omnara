import { profileAppSetup } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import { fakeId, projectApp } from '@/test/fixtures'

import { projectAppFormRequest, projectAppFormValues } from './projectAppFormState'
import { slackOAuthErrorDescription } from './slackOAuthErrors'

const profileId = fakeId('aprf'),
  second = `aprf_${'b'.repeat(26)}`
const app = projectApp({
  ...profileAppSetup({
    name: 'shared-slack',
    appType: 'slack_thread',
    scopeRef: 'T123',
    profileId,
  }),
  state: 'active',
  provider_tenant_id: 'T123',
})
const advanced = {
  ...app,
  settings: {
    launcher: {
      trigger: 'mention',
      scope_kind: 'thread',
      scope_ref: 'C123:1.2',
      slots: [
        { key: 'named', agent_profile_id: profileId },
        { key: 'repeated', agent_profile_id: profileId },
        { key: 'existing', agent_id: fakeId('agt') },
      ],
    },
  },
}

describe('app metadata form', () => {
  it('creates a draft before configuring credentials or launching agents', () => {
    expect(projectAppFormValues('slack_thread')).toMatchObject({ launcher: false, profileIds: [] })
    expect(projectAppFormRequest('slack_thread', projectAppFormValues('slack_thread'))).toEqual({
      name: 'slack-thread',
      app_type: 'slack_thread',
      settings: {},
    })
  })
  it('preserves an advanced launcher and immutable identity without mutating the app', () => {
    const before = structuredClone(advanced)
    expect(
      projectAppFormRequest(
        'slack_thread',
        projectAppFormValues('slack_thread', advanced),
        advanced,
      ),
    ).toEqual({
      name: advanced.name,
      app_type: advanced.app_type,
      settings: advanced.settings,
    })
    expect(() =>
      projectAppFormRequest(
        'slack_thread',
        { ...projectAppFormValues('slack_thread', app), name: 'renamed' },
        app,
      ),
    ).toThrow(/cannot be changed/)
    expect(() =>
      projectAppFormRequest('discord_thread', projectAppFormValues('discord_thread', app), app),
    ).toThrow(/type/)
    expect(advanced).toEqual(before)
  })
  it('keeps named, repeated and existing-agent slots when editing profiles', () => {
    const result = projectAppFormRequest(
      'slack_thread',
      { ...projectAppFormValues('slack_thread', advanced), profileIds: [profileId, second] },
      advanced,
    )
    expect(result.settings.launcher?.slots).toEqual([
      ...advanced.settings.launcher.slots,
      { key: 'profile_1', agent_profile_id: second },
    ])
    expect(
      projectAppFormRequest(
        'slack_thread',
        { ...projectAppFormValues('slack_thread', advanced), profileIds: [] },
        advanced,
      ).settings.launcher?.slots,
    ).toEqual([advanced.settings.launcher.slots[2]])
    expect(
      projectAppFormRequest(
        'slack_thread',
        { ...projectAppFormValues('slack_thread', app), profileIds: [] },
        app,
      ).settings,
    ).toEqual({})
  })
  it.each(['slack_thread', 'discord_thread'] as const)(
    'adds or removes only the %s launcher',
    (appType) => {
      const draft = {
        ...app,
        app_type: appType,
        settings: {},
        provider_config: { public_key: 'ab'.repeat(32) },
      }
      const initial = projectAppFormValues(appType, draft)
      expect(initial.scopeKind).toBe(appType === 'slack_thread' ? 'workspace' : '')
      expect(projectAppFormRequest(appType, initial, draft).settings).toEqual({})
      const request = projectAppFormRequest(
        appType,
        {
          ...initial,
          scopeRef: appType === 'slack_thread' ? 'T123' : '',
          profileIds: [profileId],
        },
        draft,
      )
      expect(request.settings.launcher).toEqual({
        ...app.settings.launcher,
        scope_kind: initial.scopeKind || undefined,
        scope_ref: appType === 'slack_thread' ? 'T123' : undefined,
      })
      const saved = { ...draft, ...request }
      expect(
        projectAppFormRequest(
          appType,
          { ...projectAppFormValues(appType, saved), profileIds: [] },
          saved,
        ).settings,
      ).toEqual({})
    },
  )
  it('defaults a new GitHub launcher to the verified installation while leaving launch disabled', () => {
    const github = projectApp({
      app_type: 'github_pr',
      state: 'active',
      provider_tenant_id: '111',
      provider_account_ref: '222',
    })
    const initial = projectAppFormValues('github_pr', github)
    expect(initial).toMatchObject({
      launcher: false,
      scopeKind: 'installation',
      scopeRef: '222',
      trigger: 'pull_request_opened',
    })
    expect(projectAppFormRequest('github_pr', initial, github).settings).toEqual({})
    expect(
      projectAppFormRequest(
        'github_pr',
        { ...initial, launcher: true, profileIds: [profileId] },
        github,
      ).settings.launcher,
    ).toEqual({
      trigger: 'pull_request_opened',
      scope_kind: 'installation',
      scope_ref: '222',
      slots: [{ key: 'default', agent_profile_id: profileId }],
    })
  })
  it('keeps saved GitHub repository restrictions and advanced slots when editing the trigger', () => {
    const github = projectApp({
      app_type: 'github_pr',
      provider_account_ref: '222',
      settings: {
        launcher: {
          trigger: 'mention',
          scope_kind: 'repository',
          scope_ref: '123',
          slots: [
            { key: 'named', agent_profile_id: profileId },
            { key: 'repeat', agent_profile_id: profileId },
            { key: 'existing', agent_id: fakeId('agt') },
          ],
        },
      },
    })
    const initial = projectAppFormValues('github_pr', github)
    expect(
      projectAppFormRequest('github_pr', { ...initial, trigger: 'pull_request_opened' }, github)
        .settings.launcher,
    ).toEqual({ ...github.settings.launcher, trigger: 'pull_request_opened' })
    expect(() =>
      projectAppFormRequest('github_pr', { ...initial, profileIds: [second] }, github),
    ).toThrow('Edit advanced GitHub launch slots through the API.')
  })
  it('retains a GitHub slot key when changing its profile', () => {
    const github = projectApp({
      app_type: 'github_pr',
      settings: {
        launcher: {
          trigger: 'mention',
          scope_kind: 'repository',
          scope_ref: '123',
          slots: [{ key: 'reviewer', agent_profile_id: profileId }],
        },
      },
    })
    const request = projectAppFormRequest(
      'github_pr',
      {
        ...projectAppFormValues('github_pr', github),
        profileIds: [second],
        trigger: 'pull_request_opened',
      },
      github,
    )
    expect(request.settings.launcher).toEqual({
      ...github.settings.launcher,
      trigger: 'pull_request_opened',
      slots: [{ key: 'reviewer', agent_profile_id: second }],
    })
  })
  it('validates changed scope and trigger on a launcher with only existing-agent slots', () => {
    const fixed = projectApp({
      ...app,
      settings: {
        launcher: {
          trigger: 'mention',
          scope_kind: 'workspace',
          scope_ref: 'T123',
          slots: [{ key: 'existing', agent_id: fakeId('agt') }],
        },
      },
    })
    const values = projectAppFormValues('slack_thread', fixed)
    expect(
      projectAppFormRequest(
        'slack_thread',
        { ...values, scopeKind: 'channel', scopeRef: ' C456 ' },
        fixed,
      ).settings.launcher,
    ).toEqual({ ...fixed.settings.launcher, scope_kind: 'channel', scope_ref: 'C456' })
    expect(() =>
      projectAppFormRequest('slack_thread', { ...values, scopeKind: 'channel' }, fixed),
    ).toThrow(/channel ID/)
    expect(() =>
      projectAppFormRequest('slack_thread', { ...values, trigger: 'pull_request_opened' }, fixed),
    ).toThrow(/trigger/)
  })

  it('reports readable name, scope and Discord key errors', () => {
    expect(() =>
      projectAppFormRequest('slack_thread', {
        ...projectAppFormValues('slack_thread'),
        name: 'not valid',
      }),
    ).toThrow(/1–32/)
    expect(() =>
      projectAppFormRequest(
        'slack_thread',
        { ...projectAppFormValues('slack_thread', app), scopeKind: 'channel', scopeRef: 'invalid' },
        app,
      ),
    ).toThrow(/Slack channel ID/)
    const discord = projectApp({
      app_type: 'discord_thread',
      settings: {
        launcher: {
          trigger: 'mention',
          slots: [{ key: 'default', agent_profile_id: profileId }],
        },
      },
    })
    expect(() =>
      projectAppFormRequest(
        'discord_thread',
        projectAppFormValues('discord_thread', discord),
        discord,
      ),
    ).toThrow(/Discord public key/)
    expect(slackOAuthErrorDescription('setup_save_failed')).not.toMatch(/already connected/i)
  })
})
