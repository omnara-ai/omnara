/** @vitest-environment happy-dom */
import {
  OmnaraClientProvider,
  useAppDefinitions,
  useConfigureProjectApp,
  useCronTriggers,
  useDeleteProjectApp,
  useDisconnectProjectApp,
  useProjectApp,
} from '@omnara/react'
import { createOmnaraClient, type CronTrigger } from '@omnara/sdk'
import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { type FakeApi, fakeApi } from '@/test/fake-api'
import { appDefinition, fakeId, projectApp } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, waitForUI } from '@/test/secret-editor'

const orgID = fakeId('org'),
  projectID = fakeId('proj')
const path = `/api/v1/orgs/${orgID}/projects/${projectID}`
const app = projectApp()
let root: Root, container: HTMLDivElement, cache: QueryClient, restore: () => void
beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
  cache = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  cache.clear()
  focusManager.setFocused(undefined)
  container.remove()
  restore()
})
function render(api: FakeApi, node: ReactNode) {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={cache}>{node}</QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })
}
function Catalog() {
  const query = useAppDefinitions(orgID, projectID)
  return <div>{query.data?.data.map((definition) => definition.app_type).join(',')}</div>
}
it('loads the registry through the project-scoped catalog hook', async () => {
  const api = fakeApi([
    {
      method: 'GET',
      path: path + '/app-definitions',
      respond: () => Response.json({ data: [appDefinition()] }),
    },
  ])
  render(api, <Catalog />)
  await waitForUI(() => {
    expect(container.textContent).toContain('slack_thread')
  })
  expect(api.requestsTo('GET', path + '/app-definitions')).toHaveLength(1)
})
function Setup() {
  const detail = useProjectApp(orgID, projectID, app.id)
  const configure = useConfigureProjectApp(orgID, projectID)
  const disconnect = useDisconnectProjectApp(orgID, projectID)
  return (
    <div>
      <output>
        {detail.data?.state}/{detail.data?.setup_revision}
      </output>
      <button
        onClick={() => {
          configure.mutate({
            appID: app.id,
            expected_setup_revision: detail.data?.setup_revision ?? 1,
            provider_tenant_id: 'T123',
            provider_account_ref: 'A123',
            credential_secret_id: fakeId('sec'),
          })
        }}
      >
        Configure
      </button>
      <button
        onClick={() => {
          disconnect.mutate(app.id)
        }}
      >
        Disconnect
      </button>
    </div>
  )
}
it('uses app setup and disconnect responses to refresh the same detail cache', async () => {
  const api = fakeApi([
    { method: 'GET', path: path + '/apps/' + app.id, respond: () => Response.json(app) },
    {
      method: 'POST',
      path: path + '/apps/' + app.id + '/setup',
      respond: () => Response.json({ ...app, state: 'active', setup_revision: 2 }),
    },
    {
      method: 'POST',
      path: path + '/apps/' + app.id + '/disconnect',
      respond: () => Response.json({ ...app, state: 'disconnected', setup_revision: 3 }),
    },
  ])
  render(api, <Setup />)
  await waitForUI(() => {
    expect(container.querySelector('output')?.textContent).toBe('disconnected/1')
  })
  act(() => {
    button('Configure').click()
  })
  await waitForUI(() => {
    expect(container.querySelector('output')?.textContent).toBe('active/2')
  })
  expect(api.requestsTo('POST', path + '/apps/' + app.id + '/setup')[0]?.body).toEqual({
    expected_setup_revision: 1,
    provider_tenant_id: 'T123',
    provider_account_ref: 'A123',
    credential_secret_id: fakeId('sec'),
  })
  act(() => {
    button('Disconnect').click()
  })
  await waitForUI(() => {
    expect(container.querySelector('output')?.textContent).toBe('disconnected/3')
  })
  expect(api.requestsTo('GET', path + '/apps/' + app.id)).toHaveLength(1)
})

it('refreshes app details on tab focus even within the 30-second freshness window', async () => {
  cache.setDefaultOptions({ queries: { retry: false, staleTime: 30_000 } })
  let current = app
  const appPath = path + '/apps/' + app.id
  const api = fakeApi([{ method: 'GET', path: appPath, respond: () => Response.json(current) }])
  render(api, <Setup />)
  await waitForUI(() => {
    expect(container.querySelector('output')?.textContent).toBe('disconnected/1')
  })
  act(() => {
    focusManager.setFocused(false)
  })
  // Another tab completes OAuth while this tab still considers its cached read fresh.
  current = {
    ...app,
    state: 'active',
    setup_revision: 2,
    provider_tenant_id: 'T123',
    provider_account_ref: 'A123',
  }
  expect(cache.getQueryCache().getAll()[0]?.isStale()).toBe(false)
  expect(api.requestsTo('GET', appPath)).toHaveLength(1)
  act(() => {
    focusManager.setFocused(true)
  })
  await waitForUI(() => {
    expect(container.querySelector('output')?.textContent).toBe('active/2')
  })
  expect(api.requestsTo('GET', appPath)).toHaveLength(2)
  expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
})

const otherProjectID = `proj_${'b'.repeat(26)}`
function AppScheduleLists() {
  const profileSchedules = useCronTriggers(orgID, projectID, {
    filters: { agent_profile_id: fakeId('aprf') },
  })
  const appSchedules = useCronTriggers(orgID, projectID, { filters: { app_id: app.id } })
  const otherSchedules = useCronTriggers(orgID, otherProjectID)
  const remove = useDeleteProjectApp(orgID, projectID)
  return (
    <>
      {[
        { label: 'Profile schedules', query: profileSchedules },
        { label: 'App schedules', query: appSchedules },
        { label: 'Other project schedules', query: otherSchedules },
      ].map(({ label, query }) => (
        <output key={label} aria-label={label}>
          {query.data?.pages
            .flatMap((page) => page.data.map((schedule) => schedule.name))
            .join(',')}
        </output>
      ))}
      <button
        onClick={() => {
          remove.mutate(app.id)
        }}
        disabled={remove.isPending}
      >
        Delete app
      </button>
      {remove.isSuccess && <p>App deleted</p>}
    </>
  )
}

it('refreshes app and profile cron lists after app deletion without invalidating another project', async () => {
  const scheduled: CronTrigger = {
    id: fakeId('cron'),
    org_id: orgID,
    project_id: projectID,
    name: 'app-schedule',
    target: {
      type: 'app_launch',
      app_id: app.id,
      agent_profile_id: fakeId('aprf'),
      destination: { channel_id: 'C123' },
      opening_message_template: 'Daily update',
    },
    cron: '0 9 * * *',
    timezone: 'UTC',
    message_template: 'Write a report.',
    enabled: true,
    last_fired_at: null,
    next_fire_at: null,
    failure_report: null,
    last_run: null,
    created_at: app.created_at,
    updated_at: app.updated_at,
  }
  const ordinary: CronTrigger = {
    ...scheduled,
    id: `cron_${'b'.repeat(26)}`,
    name: 'ordinary-schedule',
    target: { type: 'profile', agent_profile_id: fakeId('aprf') },
  }
  const otherPath = `/api/v1/orgs/${orgID}/projects/${otherProjectID}/cron-triggers`
  let deleted = false
  const api = fakeApi([
    {
      method: 'GET',
      path: path + '/cron-triggers',
      respond: ({ url }) =>
        Response.json({
          data: url.searchParams.has('app_id')
            ? deleted
              ? []
              : [scheduled]
            : deleted
              ? [ordinary]
              : [scheduled, ordinary],
          next_cursor: null,
        }),
    },
    {
      method: 'GET',
      path: otherPath,
      respond: () =>
        Response.json({
          data: [{ ...ordinary, project_id: otherProjectID, name: 'other-project-schedule' }],
          next_cursor: null,
        }),
    },
    {
      method: 'DELETE',
      path: path + '/apps/' + app.id,
      respond: () => {
        deleted = true
        return new Response(null, { status: 204 })
      },
    },
  ])
  render(api, <AppScheduleLists />)
  const names = (label: string) =>
    container.querySelector(`output[aria-label="${label}"]`)?.textContent
  await waitForUI(() => {
    expect(names('Profile schedules')).toBe('app-schedule,ordinary-schedule')
    expect(names('App schedules')).toBe('app-schedule')
    expect(names('Other project schedules')).toBe('other-project-schedule')
  })
  act(() => {
    button('Delete app').click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('App deleted')
    expect(names('Profile schedules')).toBe('ordinary-schedule')
    expect(names('App schedules')).toBe('')
  })
  expect(api.requestsTo('DELETE', path + '/apps/' + app.id)).toHaveLength(1)
  const reads = api.requestsTo('GET', path + '/cron-triggers')
  expect(
    reads.filter((request) => request.url.searchParams.get('agent_profile_id') === fakeId('aprf')),
  ).toHaveLength(2)
  expect(reads.filter((request) => request.url.searchParams.get('app_id') === app.id)).toHaveLength(
    2,
  )
  expect(api.requestsTo('GET', otherPath)).toHaveLength(1)
  expect(names('Other project schedules')).toBe('other-project-schedule')
})
