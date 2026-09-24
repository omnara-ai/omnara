import { profileIntegrationSetup } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import { fakeId, projectIntegration } from '@/test/fixtures'

import {
  projectIntegrationFormRequest,
  projectIntegrationFormValues,
} from './projectIntegrationFormState'
import { slackOAuthErrorDescription } from './slackOAuthErrors'

const profileId = fakeId('aprf'),
  second = `aprf_${'b'.repeat(26)}`
const integration = projectIntegration({
  ...profileIntegrationSetup({
    name: 'shared-slack',
    integrationType: 'slack_thread',
    scopeRef: 'T123',
    profileId,
  }),
  state: 'active',
  provider_tenant_id: 'T123',
})
const advanced = {
  ...integration,
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

describe('integration metadata form', () => {
  it('creates a draft before configuring credentials or launching agents', () => {
    expect(projectIntegrationFormValues('slack_thread')).toMatchObject({
      launcher: false,
      profileIds: [],
    })
    expect(
      projectIntegrationFormRequest('slack_thread', projectIntegrationFormValues('slack_thread')),
    ).toEqual({
      name: 'slack-thread',
      integration_type: 'slack_thread',
      settings: {},
    })
  })
  it('preserves an advanced launcher and immutable identity without mutating the integration', () => {
    const before = structuredClone(advanced)
    expect(
      projectIntegrationFormRequest(
        'slack_thread',
        projectIntegrationFormValues('slack_thread', advanced),
        advanced,
      ),
    ).toEqual({
      name: advanced.name,
      integration_type: advanced.integration_type,
      settings: advanced.settings,
    })
    expect(() =>
      projectIntegrationFormRequest(
        'slack_thread',
        { ...projectIntegrationFormValues('slack_thread', integration), name: 'renamed' },
        integration,
      ),
    ).toThrow(/cannot be changed/)
    expect(() =>
      projectIntegrationFormRequest(
        'discord_thread',
        projectIntegrationFormValues('discord_thread', integration),
        integration,
      ),
    ).toThrow(/type/)
    expect(advanced).toEqual(before)
  })
  it('keeps named, repeated and existing-agent slots when editing profiles', () => {
    const result = projectIntegrationFormRequest(
      'slack_thread',
      {
        ...projectIntegrationFormValues('slack_thread', advanced),
        profileIds: [profileId, second],
      },
      advanced,
    )
    expect(result.settings.launcher?.slots).toEqual([
      ...advanced.settings.launcher.slots,
      { key: 'profile_1', agent_profile_id: second },
    ])
    expect(
      projectIntegrationFormRequest(
        'slack_thread',
        { ...projectIntegrationFormValues('slack_thread', advanced), profileIds: [] },
        advanced,
      ).settings.launcher?.slots,
    ).toEqual([advanced.settings.launcher.slots[2]])
    expect(
      projectIntegrationFormRequest(
        'slack_thread',
        { ...projectIntegrationFormValues('slack_thread', integration), profileIds: [] },
        integration,
      ).settings,
    ).toEqual({})
  })
  it.each(['slack_thread', 'discord_thread'] as const)(
    'adds or removes only the %s launcher',
    (integrationType) => {
      const draft = {
        ...integration,
        integration_type: integrationType,
        settings: {},
        provider_config: { public_key: 'ab'.repeat(32) },
      }
      const initial = projectIntegrationFormValues(integrationType, draft)
      expect(initial.scopeKind).toBe(integrationType === 'slack_thread' ? 'workspace' : '')
      expect(projectIntegrationFormRequest(integrationType, initial, draft).settings).toEqual({})
      const request = projectIntegrationFormRequest(
        integrationType,
        {
          ...initial,
          scopeRef: integrationType === 'slack_thread' ? 'T123' : '',
          profileIds: [profileId],
        },
        draft,
      )
      expect(request.settings.launcher).toEqual({
        ...integration.settings.launcher,
        scope_kind: initial.scopeKind || undefined,
        scope_ref: integrationType === 'slack_thread' ? 'T123' : undefined,
      })
      const saved = { ...draft, ...request }
      expect(
        projectIntegrationFormRequest(
          integrationType,
          { ...projectIntegrationFormValues(integrationType, saved), profileIds: [] },
          saved,
        ).settings,
      ).toEqual({})
    },
  )
  it('defaults a new GitHub launcher to the verified installation while leaving launch disabled', () => {
    const github = projectIntegration({
      integration_type: 'github_pr',
      state: 'active',
      provider_tenant_id: '111',
      provider_account_ref: '222',
    })
    const initial = projectIntegrationFormValues('github_pr', github)
    expect(initial).toMatchObject({
      launcher: false,
      scopeKind: 'installation',
      scopeRef: '222',
      trigger: 'pull_request_opened',
    })
    expect(projectIntegrationFormRequest('github_pr', initial, github).settings).toEqual({})
    expect(
      projectIntegrationFormRequest(
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
    const github = projectIntegration({
      integration_type: 'github_pr',
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
    const initial = projectIntegrationFormValues('github_pr', github)
    expect(
      projectIntegrationFormRequest(
        'github_pr',
        { ...initial, trigger: 'pull_request_opened' },
        github,
      ).settings.launcher,
    ).toEqual({ ...github.settings.launcher, trigger: 'pull_request_opened' })
    expect(() =>
      projectIntegrationFormRequest('github_pr', { ...initial, profileIds: [second] }, github),
    ).toThrow('Edit advanced GitHub launch slots through the API.')
  })
  it('retains a GitHub slot key when changing its profile', () => {
    const github = projectIntegration({
      integration_type: 'github_pr',
      settings: {
        launcher: {
          trigger: 'mention',
          scope_kind: 'repository',
          scope_ref: '123',
          slots: [{ key: 'reviewer', agent_profile_id: profileId }],
        },
      },
    })
    const request = projectIntegrationFormRequest(
      'github_pr',
      {
        ...projectIntegrationFormValues('github_pr', github),
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
    const fixed = projectIntegration({
      ...integration,
      settings: {
        launcher: {
          trigger: 'mention',
          scope_kind: 'workspace',
          scope_ref: 'T123',
          slots: [{ key: 'existing', agent_id: fakeId('agt') }],
        },
      },
    })
    const values = projectIntegrationFormValues('slack_thread', fixed)
    expect(
      projectIntegrationFormRequest(
        'slack_thread',
        { ...values, scopeKind: 'channel', scopeRef: ' C456 ' },
        fixed,
      ).settings.launcher,
    ).toEqual({ ...fixed.settings.launcher, scope_kind: 'channel', scope_ref: 'C456' })
    expect(() =>
      projectIntegrationFormRequest('slack_thread', { ...values, scopeKind: 'channel' }, fixed),
    ).toThrow(/channel ID/)
    expect(() =>
      projectIntegrationFormRequest(
        'slack_thread',
        { ...values, trigger: 'pull_request_opened' },
        fixed,
      ),
    ).toThrow(/trigger/)
  })

  it('reports readable name, scope and Discord key errors', () => {
    expect(() =>
      projectIntegrationFormRequest('slack_thread', {
        ...projectIntegrationFormValues('slack_thread'),
        name: 'not valid',
      }),
    ).toThrow(/1–32/)
    expect(() =>
      projectIntegrationFormRequest(
        'slack_thread',
        {
          ...projectIntegrationFormValues('slack_thread', integration),
          scopeKind: 'channel',
          scopeRef: 'invalid',
        },
        integration,
      ),
    ).toThrow(/Slack channel ID/)
    const discord = projectIntegration({
      integration_type: 'discord_thread',
      settings: {
        launcher: {
          trigger: 'mention',
          slots: [{ key: 'default', agent_profile_id: profileId }],
        },
      },
    })
    expect(() =>
      projectIntegrationFormRequest(
        'discord_thread',
        projectIntegrationFormValues('discord_thread', discord),
        discord,
      ),
    ).toThrow(/Discord public key/)
    expect(slackOAuthErrorDescription('setup_save_failed')).not.toMatch(/already connected/i)
  })
})
