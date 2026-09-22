/** @vitest-environment happy-dom */

import { type CronTrigger, type CronTriggerTarget, schemas } from '@omnara/sdk'
import { act } from 'react'
import { expect, it, vi } from 'vitest'

import { formatDateTime } from '@/lib/format'
import { ProjectAppDetail } from '@/routes/ProjectAppPage'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId, projectApp } from '@/test/fixtures'
import { button, enter, field, waitForUI } from '@/test/secret-editor'

import {
  appTarget,
  cache,
  chooseProfile,
  container,
  cronPath,
  now,
  orgId,
  path,
  profile,
  profileRoutes,
  projectId,
  render,
  submit,
  trigger,
} from './cron-trigger-test-fixture'
import { CreateCronTriggerDialog, EditCronTriggerDialog } from './CronTriggerDialog'
import { CronTriggersList } from './CronTriggersSection'

it.each(['slack_thread', 'discord_thread'] as const)(
  'creates a %s app schedule without a mention launcher',
  async (appType) => {
    const app = projectApp({ app_type: appType, state: 'active' })
    let schedules: CronTrigger[] = []
    const api = fakeApi([
      ...profileRoutes,
      { method: 'GET', path: path + '/apps/' + app.id, respond: () => Response.json(app) },
      {
        method: 'GET',
        path: cronPath,
        respond: () => Response.json({ data: schedules, next_cursor: null }),
      },
      {
        method: 'POST',
        path: cronPath,
        respond: ({ body }) => {
          const created = trigger(schemas.zCreateCronTriggerRequest.parse(body))
          schedules = [created]
          return Response.json(created, { status: 201 })
        },
      },
    ])
    render(api, <ProjectAppDetail orgId={orgId} projectId={projectId} appId={app.id} canManage />)
    await waitForUI(() => {
      expect(button('Add schedule')).toBeDefined()
    })
    expect(container.textContent).toContain('Start a fresh agent in a new channel thread')
    act(() => {
      button('Add schedule').click()
    })
    expect(field('Opening message').value).toBe('{{.trigger.name}} — {{.trigger.local_date}}')
    expect(button('Create schedule').disabled).toBe(true)
    await enter('Name', 'morning-report')
    await enter('Cron expression', '0 9 * * 1-5')
    await enter('Task instructions', 'Summarize yesterday’s progress.')
    await enter('Channel ID', appType === 'slack_thread' ? 'G123' : '123456789')
    expect(document.body.textContent).not.toContain('Server ID')
    expect(button('Create schedule').disabled).toBe(true)
    await chooseProfile()
    await submit()
    await waitForUI(() => {
      expect(document.querySelector('[role="dialog"]')).toBeNull()
    })
    expect(api.requestsTo('POST', cronPath)[0]?.body).toEqual({
      name: 'morning-report',
      target: {
        type: 'app',
        app_id: app.id,
        settings: {
          agent_profile_id: profile.id,
          channel_id: appType === 'slack_thread' ? 'G123' : '123456789',
          opening_message_template: '{{.trigger.name}} — {{.trigger.local_date}}',
          message_template: 'Summarize yesterday’s progress.',
        },
      },
      cron: '0 9 * * 1-5',
      timezone: new Intl.DateTimeFormat().resolvedOptions().timeZone,
    })
    expect(
      api
        .requestsTo('GET', cronPath)
        .every((request) => request.url.searchParams.get('app_id') === app.id),
    ).toBe(true)
    expect(api.requests.filter((request) => request.method !== 'GET')).toHaveLength(1)
    expect(container.textContent).toContain('morning-report')
  },
)

it('keeps mentions and schedules available together without changing the launcher', async () => {
  const app = projectApp({
    state: 'active',
    settings: {
      launcher: {
        trigger: 'mention',
        scope_kind: 'channel',
        scope_ref: 'C123',
        slots: [{ key: 'default', agent_profile_id: profile.id }],
      },
    },
  })
  const api = fakeApi([
    ...profileRoutes,
    { method: 'GET', path: path + '/apps/' + app.id, respond: () => Response.json(app) },
    {
      method: 'GET',
      path: cronPath,
      respond: () => Response.json({ data: [trigger()], next_cursor: null }),
    },
  ])
  render(api, <ProjectAppDetail orgId={orgId} projectId={projectId} appId={app.id} canManage />)
  await waitForUI(() => {
    expect(container.textContent).toContain('daily-report')
  })
  expect(container.textContent).toContain('When someone mentions the bot')
  act(() => {
    button('Add schedule').click()
  })
  expect(document.querySelector('[role="dialog"]')).not.toBeNull()
  expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
})

it.each(['github_pr', 'future_app'] as const)(
  'does not offer schedules for %s apps',
  async (appType) => {
    const app = { ...projectApp({ app_type: 'github_pr' }), app_type: appType }
    const api = fakeApi([
      { method: 'GET', path: path + '/apps/' + app.id, respond: () => Response.json(app) },
    ])
    render(api, <ProjectAppDetail orgId={orgId} projectId={projectId} appId={app.id} canManage />)
    await waitForUI(() => {
      expect(container.querySelector('h1')?.textContent).toBe(app.name)
    })
    expect(container.querySelector('[aria-label="Schedules"]')).toBeNull()
    expect(api.requestsTo('GET', cronPath)).toHaveLength(0)
  },
)

it('edits app settings including the profile for future runs while keeping the saved app', async () => {
  const app = projectApp({ app_type: 'discord_thread', state: 'active' })
  const saved = trigger({
    target: { ...appTarget, settings: { ...appTarget.settings, channel_id: '123' } },
  })
  const replacement = { ...profile, id: `aprf_${'b'.repeat(26)}`, name: 'Weekly reporter' }
  const onOpenChange = vi.fn()
  const api = fakeApi([
    {
      method: 'GET',
      path: path + '/agent-profiles',
      respond: () => Response.json({ data: [profile, replacement], next_cursor: null }),
    },
    ...profileRoutes,
    { method: 'GET', path: path + '/apps/' + app.id, respond: () => Response.json(app) },
    {
      method: 'PATCH',
      path: cronPath + '/' + saved.id,
      respond: ({ body }) =>
        Response.json({ ...saved, ...schemas.zUpdateCronTriggerRequest.parse(body) }),
    },
  ])
  render(
    api,
    <EditCronTriggerDialog
      open
      onOpenChange={onOpenChange}
      orgId={orgId}
      projectId={projectId}
      trigger={saved}
    />,
  )
  await waitForUI(() => {
    expect(field('Channel ID').value).toBe('123')
  })
  expect(document.querySelector<HTMLButtonElement>('#app-profiles')?.disabled).toBe(false)
  expect(document.body.textContent).not.toContain('Server ID')
  await chooseProfile(replacement)
  await enter('Channel ID', '789')
  await enter('Opening message', 'Daily update {{.trigger.local_date}}')
  await enter('Task instructions', 'Write a detailed update.')
  await submit()
  await waitForUI(() => {
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })
  expect(api.requestsTo('PATCH', cronPath + '/' + saved.id)[0]?.body).toEqual({
    name: saved.name,
    cron: saved.cron,
    timezone: saved.timezone,
    target: {
      type: 'app',
      app_id: app.id,
      settings: {
        agent_profile_id: replacement.id,
        channel_id: '789',
        opening_message_template: 'Daily update {{.trigger.local_date}}',
        message_template: 'Write a detailed update.',
      },
    },
  })
})

it('rejects Slack DMs and thread addresses before sending an update and retains drafts after API errors', async () => {
  const app = projectApp({ state: 'active' }),
    saved = trigger()
  const api = fakeApi([
    ...profileRoutes,
    { method: 'GET', path: path + '/apps/' + app.id, respond: () => Response.json(app) },
    {
      method: 'PATCH',
      path: cronPath + '/' + saved.id,
      respond: () =>
        jsonResponse({ code: 'invalid_argument', error: 'Bot cannot access this channel' }, 400),
    },
  ])
  render(
    api,
    <EditCronTriggerDialog
      open
      onOpenChange={vi.fn()}
      orgId={orgId}
      projectId={projectId}
      trigger={saved}
    />,
  )
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Slack channel ID')
  })
  for (const invalid of ['D123', 'C123/1234.567', '123456']) {
    await enter('Channel ID', invalid)
    await submit()
  }
  expect(api.requestsTo('PATCH', cronPath + '/' + saved.id)).toHaveLength(0)
  await enter('Channel ID', 'C456')
  await enter('Opening message', 'Keep this draft')
  await submit()
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Bot cannot access this channel')
  })
  expect(field('Opening message').value).toBe('Keep this draft')
  expect(field('Channel ID').value).toBe('C456')
})

it.each([
  { canManage: true, state: 'active' },
  { canManage: false, state: 'active' },
  { canManage: true, state: 'disconnected' },
  { canManage: false, state: 'disconnected' },
] as const)(
  'honors app schedule management permissions (canManage=$canManage, state=$state)',
  async ({ canManage, state }) => {
    const app = projectApp({ state })
    let saved = trigger(),
      deleted = false
    const api = fakeApi([
      ...profileRoutes,
      { method: 'GET', path: path + '/apps/' + app.id, respond: () => Response.json(app) },
      {
        method: 'GET',
        path: cronPath,
        respond: () => Response.json({ data: deleted ? [] : [saved], next_cursor: null }),
      },
      {
        method: 'PATCH',
        path: cronPath + '/' + saved.id,
        respond: ({ body }) => {
          saved = { ...saved, ...schemas.zUpdateCronTriggerRequest.parse(body) }
          return Response.json(saved)
        },
      },
      {
        method: 'DELETE',
        path: cronPath + '/' + saved.id,
        respond: () => {
          deleted = true
          return new Response(null, { status: 204 })
        },
      },
    ])
    render(
      api,
      <ProjectAppDetail orgId={orgId} projectId={projectId} appId={app.id} canManage={canManage} />,
    )
    await waitForUI(() => {
      expect(container.textContent).toContain(saved.name)
    })
    const edit = button('Edit schedule daily-report'),
      toggle = button('Disable schedule daily-report'),
      remove = button('Delete schedule daily-report')
    expect(edit.disabled).toBe(!canManage)
    expect(toggle.disabled).toBe(!canManage)
    expect(remove.disabled).toBe(!canManage)
    if (!canManage) {
      expect(container.textContent).not.toContain('Add schedule')
      act(() => {
        edit.click()
        toggle.click()
        remove.click()
      })
      expect(document.querySelector('[role="dialog"]')).toBeNull()
      expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
      return
    }
    expect(button('Add schedule').disabled).toBe(state !== 'active')
    if (state === 'disconnected') {
      expect(container.textContent).toContain(
        'Connect this app before creating schedules or running scheduled actions.',
      )
      act(() => {
        button('Add schedule').click()
      })
      expect(document.querySelector('[role="dialog"]')).toBeNull()
      expect(api.requestsTo('POST', cronPath)).toHaveLength(0)
    }
    act(() => {
      edit.click()
    })
    expect(document.querySelector('[role="dialog"]')).not.toBeNull()
    act(() => {
      button('Close').click()
    })
    act(() => {
      toggle.click()
    })
    await waitForUI(() => {
      expect(button('Enable schedule daily-report')).toBeDefined()
    })
    act(() => {
      button('Enable schedule daily-report').click()
    })
    await waitForUI(() => {
      expect(button('Disable schedule daily-report')).toBeDefined()
    })
    expect(
      api.requestsTo('PATCH', cronPath + '/' + saved.id).map((request) => request.body),
    ).toEqual([{ enabled: false }, { enabled: true }])
    vi.stubGlobal('confirm', () => true)
    act(() => {
      remove.click()
    })
    await waitForUI(() => {
      expect(container.textContent).not.toContain(saved.name)
      if (state === 'active') expect(container.textContent).toContain('No schedules yet.')
      else expect(container.querySelector('[aria-label="Schedules"]')).toBeNull()
    })
    expect(api.requestsTo('DELETE', cronPath + '/' + saved.id)).toHaveLength(1)
  },
)

it.each([false, true])(
  'keeps a draft schedule error visible without refetching until retry (hasSchedules=%s)',
  async (hasSchedules) => {
    vi.useFakeTimers()
    const app = projectApp()
    const saved = trigger()
    let currentApp = app
    let recovered = false
    const api = fakeApi([
      ...profileRoutes,
      { method: 'GET', path: path + '/apps/' + app.id, respond: () => Response.json(currentApp) },
      {
        method: 'GET',
        path: path + '/apps/' + app.id + '/subscriptions',
        respond: () => Response.json({ data: [], next_cursor: null }),
      },
      {
        method: 'GET',
        path: cronPath,
        respond: () =>
          recovered
            ? Response.json({ data: hasSchedules ? [saved] : [], next_cursor: null })
            : jsonResponse({ code: 'internal_error', error: 'Try again' }, 500),
      },
    ])
    render(api, <ProjectAppDetail orgId={orgId} projectId={projectId} appId={app.id} canManage />)
    await waitForUI(() => {
      expect(container.querySelector('[aria-label="Schedules"]')?.textContent).toContain(
        'load schedules',
      )
      expect(button('Retry')).toBeDefined()
    })
    // Give any mount/refetch cycle time to run; the failed query must stay idle instead.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000)
    })
    expect(api.requestsTo('GET', cronPath)).toHaveLength(1)
    expect(button('Retry')).toBeDefined()
    recovered = true
    act(() => {
      button('Retry').click()
    })
    await waitForUI(() => {
      expect(api.requestsTo('GET', cronPath)).toHaveLength(2)
      expect(cache.isFetching()).toBe(0)
      if (hasSchedules) {
        expect(container.textContent).toContain(saved.name)
        expect(button('Add schedule').disabled).toBe(true)
      } else {
        expect(container.querySelector('[aria-label="Schedules"]')).toBeNull()
      }
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000)
    })
    expect(api.requestsTo('GET', cronPath)).toHaveLength(2)
    currentApp = { ...app, state: 'active', provider_tenant_id: 'T123' }
    await act(async () => {
      await cache.invalidateQueries()
    })
    await waitForUI(() => {
      expect(button('Add schedule').disabled).toBe(false)
      expect(container.querySelector('[aria-label="Schedules"]')?.textContent).toContain(
        hasSchedules ? saved.name : 'No schedules yet.',
      )
    })
    expect(api.requestsTo('GET', cronPath)).toHaveLength(3)
    expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
  },
)

it('closes a schedule editor when management permission is removed', async () => {
  const app = projectApp({ state: 'active' })
  const api = fakeApi([
    ...profileRoutes,
    { method: 'GET', path: path + '/apps/' + app.id, respond: () => Response.json(app) },
    {
      method: 'GET',
      path: cronPath,
      respond: () => Response.json({ data: [trigger()], next_cursor: null }),
    },
  ])
  const rerender = render(
    api,
    <ProjectAppDetail orgId={orgId} projectId={projectId} appId={app.id} canManage />,
  )
  await waitForUI(() => {
    expect(button('Edit schedule daily-report')).toBeDefined()
  })
  act(() => {
    button('Edit schedule daily-report').click()
  })
  expect(document.querySelector('[role="dialog"]')).not.toBeNull()
  rerender(
    <ProjectAppDetail orgId={orgId} projectId={projectId} appId={app.id} canManage={false} />,
  )
  expect(document.querySelector('[role="dialog"]')).toBeNull()
  expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
})

it.each([
  ['queued', 'Last run: queued'],
  ['processing', 'Last run: processing'],
  ['completed', 'Last run: app action completed'],
  ['failed', 'Last run: Could not prepare the thread'],
] as const)(
  'shows %s app action state independently of firing failure reports',
  async (state, label) => {
    const earlierRun = '2026-09-19T09:00:00Z'
    const saved = trigger({
      last_fired_at: earlierRun,
      last_run: {
        state,
        created_at: earlierRun,
        updated_at: earlierRun,
        failure_message: state === 'failed' ? 'Could not prepare the thread' : null,
      },
      failure_report: { message: 'New firing failed to queue', failed_at: now, will_retry: false },
    })
    const api = fakeApi([
      {
        method: 'GET',
        path: cronPath,
        respond: () => Response.json({ data: [saved], next_cursor: null }),
      },
    ])
    render(
      api,
      <CronTriggersList
        orgId={orgId}
        projectId={projectId}
        canManage={false}
        filters={{ app_id: fakeId('app') }}
        emptyMessage="No schedules"
      />,
    )
    await waitForUI(() => {
      expect(container.textContent).toContain(label)
    })
    const runTime = container.querySelector('time')
    expect(runTime?.getAttribute('datetime')).toBe(earlierRun)
    expect(runTime?.textContent).toBe(formatDateTime(earlierRun))
    expect(runTime?.closest('p')?.textContent).toContain(label)
    expect(runTime?.closest('p')?.textContent).not.toContain(formatDateTime(now))
    expect(container.textContent).toContain('New firing failed to queue')
    expect(container.textContent).toContain(`Failed ${formatDateTime(now)}`)
    if (state === 'failed') expect(container.textContent).toContain('Could not prepare the thread')
  },
)

it('shows a generic action failure once with its update time', async () => {
  const saved = trigger({
    last_fired_at: now,
    last_run: {
      state: 'failed',
      created_at: now,
      updated_at: now,
      failure_message: 'Scheduled app action failed.',
    },
  })
  const api = fakeApi([
    {
      method: 'GET',
      path: cronPath,
      respond: () => Response.json({ data: [saved], next_cursor: null }),
    },
  ])
  render(
    api,
    <CronTriggersList
      orgId={orgId}
      projectId={projectId}
      canManage={false}
      filters={{ app_id: fakeId('app') }}
      emptyMessage="No schedules"
    />,
  )
  await waitForUI(() => {
    expect(container.textContent).toContain('Scheduled app action failed.')
  })
  expect(container.textContent.match(/failed/gi)).toHaveLength(1)
  expect(container.querySelector('time')?.textContent).toBe(formatDateTime(now))
})

it.each([null, now])(
  'does not infer failure without last_run (last_fired_at=%s)',
  async (lastFiredAt) => {
    const saved = trigger({ last_fired_at: lastFiredAt })
    const api = fakeApi([
      {
        method: 'GET',
        path: cronPath,
        respond: () => Response.json({ data: [saved], next_cursor: null }),
      },
    ])
    render(
      api,
      <CronTriggersList
        orgId={orgId}
        projectId={projectId}
        canManage={false}
        filters={{ app_id: fakeId('app') }}
        emptyMessage="No schedules"
      />,
    )
    await waitForUI(() => {
      expect(container.textContent).toContain(
        lastFiredAt ? 'Last run: details not retained' : 'Not run yet',
      )
    })
    expect(container.textContent).not.toContain('Failing')
    expect(container.textContent).not.toContain('failed')
  },
)

it.each([
  { type: 'profile', agent_profile_id: profile.id },
  { type: 'agent', agent_id: fakeId('agt') },
] satisfies CronTriggerTarget[])('preserves ordinary $type cron creation', async (target) => {
  const onCreated = vi.fn()
  const api = fakeApi([
    {
      method: 'POST',
      path: cronPath,
      respond: ({ body }) =>
        Response.json(trigger(schemas.zCreateCronTriggerRequest.parse(body)), { status: 201 }),
    },
  ])
  render(
    api,
    <CreateCronTriggerDialog
      open
      onOpenChange={vi.fn()}
      orgId={orgId}
      projectId={projectId}
      target={target}
      targetLabel="Existing target"
      onCreated={onCreated}
    />,
  )
  await enter('Name', 'reminder')
  await enter('Cron expression', '0 12 * * *')
  await enter('Message', 'Check the queue')
  expect(document.body.textContent).toContain('{{.trigger.local_date}}')
  expect(document.body.textContent).toContain('The local date uses the schedule’s timezone.')
  expect(document.body.textContent).not.toContain('Opening message')
  expect(document.body.textContent).not.toContain('Agent profile')
  await submit()
  await waitForUI(() => {
    expect(onCreated).toHaveBeenCalledOnce()
  })
  expect(api.requests[0]?.body).toEqual({
    name: 'reminder',
    cron: '0 12 * * *',
    timezone: new Intl.DateTimeFormat().resolvedOptions().timeZone,
    message_template: 'Check the queue',
    target: target.type === 'agent' ? { ...target, delivery_mode: 'queued' } : target,
  })
})

it.each([
  { type: 'profile', agent_profile_id: profile.id },
  { type: 'agent', agent_id: fakeId('agt'), delivery_mode: 'steering' },
] satisfies CronTriggerTarget[])('preserves ordinary $type cron editing', async (target) => {
  const saved = trigger({ target, message_template: 'Original prompt' }),
    onOpenChange = vi.fn()
  const api = fakeApi([
    {
      method: 'PATCH',
      path: cronPath + '/' + saved.id,
      respond: ({ body }) =>
        Response.json({ ...saved, ...schemas.zUpdateCronTriggerRequest.parse(body) }),
    },
  ])
  render(
    api,
    <EditCronTriggerDialog
      open
      onOpenChange={onOpenChange}
      orgId={orgId}
      projectId={projectId}
      trigger={saved}
    />,
  )
  await enter('Message', 'Updated prompt')
  await submit()
  await waitForUI(() => {
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })
  const expected = {
    name: saved.name,
    cron: saved.cron,
    timezone: 'UTC',
    message_template: 'Updated prompt',
  }
  expect(api.requests[0]?.body).toEqual(
    target.type === 'agent' ? { ...expected, target } : expected,
  )
})
