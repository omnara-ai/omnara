import { profileAppSetup, type ProjectApp } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import { fakeId } from '@/test/fixtures'

import { projectAppFormRequest, projectAppFormValues } from './projectAppFormState'
import { slackOAuthErrorDescription } from './SlackOAuthOutcomeDialogState'

it('does not mistake a failed Slack setup save for an existing connection', () => {
  expect(slackOAuthErrorDescription('setup_save_failed')).not.toMatch(/already connected/i)
})

const connectionId = fakeId('iin'),
  profileId = fakeId('aprf')
const second = `aprf_${'b'.repeat(26)}`
const app: ProjectApp = {
  ...profileAppSetup({
    name: 'Shared Slack',
    connectionId,
    provider: 'slack',
    scopeRef: 'T123',
    profileId,
    listen: true,
    interactions: true,
    tools: ['slack_read'],
  }),
  id: fakeId('app'),
  project_id: fakeId('proj'),
  enabled: false,
  created_at: '2026-09-18T00:00:00Z',
  updated_at: '2026-09-18T00:00:00Z',
}
const advanced: ProjectApp = {
  ...app,
  settings: {
    resource: {
      ...app.settings.resource,
      enabled: false,
      scope: { slack: { channel_id: 'C123', thread_ts: '1.2' } },
      follow: { replies: false },
      tools: {
        slack_read: {
          deferred: true,
          permission: { mode: 'always_ask', parameters: { key: ['preserved'] } },
        },
        custom: { type: 'custom', description: 'Keep me', input_schema: { type: 'object' } },
      },
      mcp: {
        tools: {
          url: 'https://example.com/mcp',
          default_enabled: false,
          permission: { mode: 'always_ask' },
        },
      },
    },
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

describe('project app form requests', () => {
  it('starts new setups with an empty profile selection and launcher on', () => {
    expect(projectAppFormValues('slack')).toMatchObject({ launcher: true, profileIds: [] })
    expect(() =>
      projectAppFormRequest('slack', projectAppFormValues('slack'), connectionId),
    ).toThrow(/profile/)
    const request = projectAppFormRequest(
      'slack',
      { ...projectAppFormValues('slack'), launcher: false },
      connectionId,
    )
    expect(request.settings.launcher).toBeUndefined()
    expect(projectAppFormValues('slack', { ...app, settings: request.settings }).launcher).toBe(
      false,
    )
  })
  it('preserves an advanced setup on no-op, rename, and reversible checkbox changes', () => {
    const before = structuredClone(advanced)
    const initial = projectAppFormValues('slack', advanced)
    expect(projectAppFormRequest('slack', initial, connectionId, advanced)).toEqual({
      name: advanced.name,
      enabled: false,
      settings: advanced.settings,
    })
    expect(
      projectAppFormRequest('slack', { ...initial, name: 'Renamed' }, connectionId, advanced),
    ).toEqual({ name: 'Renamed', enabled: false, settings: advanced.settings })
    expect(advanced).toEqual(before)
  })
  it('keeps named, repeated and existing-agent slots when adding profiles', () => {
    const result = projectAppFormRequest(
      'slack',
      { ...projectAppFormValues('slack', advanced), profileIds: [profileId, second] },
      connectionId,
      advanced,
    )
    expect(result.settings.launcher?.slots).toEqual([
      ...(advanced.settings.launcher?.slots ?? []),
      { key: 'profile_1', agent_profile_id: second },
    ])
    expect(result.settings.resource).toEqual(advanced.settings.resource)
  })
  it('allows only fixed slots to remain and rejects an empty launcher', () => {
    const result = projectAppFormRequest(
      'slack',
      { ...projectAppFormValues('slack', advanced), profileIds: [] },
      connectionId,
      advanced,
    )
    expect(result.settings.launcher?.slots).toEqual([advanced.settings.launcher?.slots[2]])
    expect(() =>
      projectAppFormRequest(
        'slack',
        { ...projectAppFormValues('slack', app), profileIds: [] },
        connectionId,
        app,
      ),
    ).toThrow(/profile/)
  })
  it('changes only selected capabilities and keeps unknown tool settings', () => {
    const result = projectAppFormRequest(
      'slack',
      { ...projectAppFormValues('slack', advanced), tools: ['slack_post_message'], listen: false },
      connectionId,
      advanced,
    )
    expect(result.settings.resource.tools).toEqual({
      custom: advanced.settings.resource.tools?.custom,
      slack_post_message: {},
    })
    expect(result.settings.resource.listener).toBeUndefined()
    expect(result.settings.resource.scope).toEqual(advanced.settings.resource.scope)
    expect(result.settings.resource.mcp).toEqual(advanced.settings.resource.mcp)
    expect(result.settings.launcher).toEqual(advanced.settings.launcher)
  })
  it('refuses changing an app provider or configured connection', () => {
    expect(() =>
      projectAppFormRequest(
        'slack',
        projectAppFormValues('slack', app),
        `iin_${'b'.repeat(26)}`,
        app,
      ),
    ).toThrow(/connection/)
    expect(() =>
      projectAppFormRequest('discord', projectAppFormValues('discord', app), connectionId, app),
    ).toThrow(/provider/)
  })
  it('can add a launcher to existing defaults without replacing resource settings', () => {
    const template = { ...advanced, settings: { resource: advanced.settings.resource } }
    const result = projectAppFormRequest(
      'slack',
      {
        ...projectAppFormValues('slack', template),
        launcher: true,
        profileIds: [profileId],
        scopeRef: 'T123',
      },
      connectionId,
      template,
    )
    expect(result.settings.resource).toEqual(template.settings.resource)
    expect(result.settings.launcher).toEqual(app.settings.launcher)
  })
  it('replaces the profile in a single GitHub slot while retaining its key', () => {
    const github: ProjectApp = {
      ...app,
      settings: {
        resource: {
          definition: 'omnara.github',
          connection: connectionId,
          listener: { events: ['commit'] },
        },
        launcher: {
          trigger: 'mention',
          scope_kind: 'repository',
          scope_ref: '123',
          slots: [{ key: 'reviewer', agent_profile_id: profileId }],
        },
      },
    }
    const result = projectAppFormRequest(
      'github',
      {
        ...projectAppFormValues('github', github),
        profileIds: [second],
        trigger: 'pull_request_opened',
      },
      connectionId,
      github,
    )
    expect(result.settings.launcher).toEqual({
      ...github.settings.launcher,
      trigger: 'pull_request_opened',
      slots: [{ key: 'reviewer', agent_profile_id: second }],
    })
    expect(result.settings.resource.listener).toEqual({ events: ['commit'] })
  })
})
