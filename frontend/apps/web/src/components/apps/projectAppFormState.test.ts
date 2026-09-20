import { profileAppSetup } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import { fakeId, projectApp } from '@/test/fixtures'

import { projectAppFormRequest, projectAppFormValues } from './projectAppFormState'
import { slackOAuthErrorDescription } from './SlackOAuthOutcomeDialogState'

const profileId = fakeId('aprf'),
  second = `aprf_${'b'.repeat(26)}`
const app = projectApp({
  ...profileAppSetup({ name: 'shared-slack', provider: 'slack', scopeRef: 'T123', profileId }),
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
    expect(projectAppFormValues('slack')).toMatchObject({ launcher: false, profileIds: [] })
    expect(projectAppFormRequest('slack', projectAppFormValues('slack'))).toEqual({
      name: 'slack',
      definition_id: 'omnara.slack',
      settings: {},
    })
  })
  it('preserves an advanced launcher and immutable identity without mutating the app', () => {
    const before = structuredClone(advanced)
    expect(
      projectAppFormRequest('slack', projectAppFormValues('slack', advanced), advanced),
    ).toEqual({
      name: advanced.name,
      definition_id: advanced.definition_id,
      settings: advanced.settings,
    })
    expect(() =>
      projectAppFormRequest(
        'slack',
        { ...projectAppFormValues('slack', app), name: 'renamed' },
        app,
      ),
    ).toThrow(/cannot be changed/)
    expect(() =>
      projectAppFormRequest('discord', projectAppFormValues('discord', app), app),
    ).toThrow(/provider/)
    expect(advanced).toEqual(before)
  })
  it('keeps named, repeated and existing-agent slots when editing profiles', () => {
    const result = projectAppFormRequest(
      'slack',
      { ...projectAppFormValues('slack', advanced), profileIds: [profileId, second] },
      advanced,
    )
    expect(result.settings.launcher?.slots).toEqual([
      ...advanced.settings.launcher.slots,
      { key: 'profile_1', agent_profile_id: second },
    ])
    expect(
      projectAppFormRequest(
        'slack',
        { ...projectAppFormValues('slack', advanced), profileIds: [] },
        advanced,
      ).settings.launcher?.slots,
    ).toEqual([advanced.settings.launcher.slots[2]])
    expect(() =>
      projectAppFormRequest(
        'slack',
        { ...projectAppFormValues('slack', app), profileIds: [] },
        app,
      ),
    ).toThrow(/profile/)
  })
  it('adds or removes only the launcher', () => {
    const draft = { ...app, settings: {} }
    const request = projectAppFormRequest(
      'slack',
      { ...projectAppFormValues('slack', draft), launcher: true, profileIds: [profileId] },
      draft,
    )
    expect(request.settings).toEqual(app.settings)
    expect(
      projectAppFormRequest(
        'slack',
        { ...projectAppFormValues('slack', app), launcher: false },
        app,
      ).settings,
    ).toEqual({})
  })
  it('retains a GitHub slot key when changing its profile', () => {
    const github = projectApp({
      provider: 'github',
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
      'github',
      {
        ...projectAppFormValues('github', github),
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
    const values = projectAppFormValues('slack', fixed)
    expect(
      projectAppFormRequest('slack', { ...values, scopeKind: 'channel', scopeRef: ' C456 ' }, fixed)
        .settings.launcher,
    ).toEqual({ ...fixed.settings.launcher, scope_kind: 'channel', scope_ref: 'C456' })
    expect(() =>
      projectAppFormRequest('slack', { ...values, scopeKind: 'channel' }, fixed),
    ).toThrow(/channel ID/)
    expect(() =>
      projectAppFormRequest('slack', { ...values, trigger: 'pull_request_opened' }, fixed),
    ).toThrow(/trigger/)
  })

  it('reports readable name, scope and Discord key errors', () => {
    expect(() =>
      projectAppFormRequest('slack', { ...projectAppFormValues('slack'), name: 'not valid' }),
    ).toThrow(/1–32/)
    expect(() =>
      projectAppFormRequest(
        'slack',
        { ...projectAppFormValues('slack', app), scopeKind: 'channel', scopeRef: 'invalid' },
        app,
      ),
    ).toThrow(/Slack channel ID/)
    const discord = projectApp({
      provider: 'discord',
      settings: {
        launcher: {
          trigger: 'mention',
          scope_kind: 'channel',
          scope_ref: '123',
          slots: [{ key: 'default', agent_profile_id: profileId }],
        },
      },
    })
    expect(() =>
      projectAppFormRequest('discord', projectAppFormValues('discord', discord), discord),
    ).toThrow(/Discord public key/)
    expect(slackOAuthErrorDescription('setup_save_failed')).not.toMatch(/already connected/i)
  })
})
