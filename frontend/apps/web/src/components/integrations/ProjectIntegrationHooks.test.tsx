/** @vitest-environment happy-dom */
import {
  OmnaraClientProvider,
  useConfigureProjectIntegration,
  useCreateProjectIntegrationGitHubSetup,
  useCronTriggers,
  useDeleteProjectIntegration,
  useDisconnectProjectIntegration,
  useIntegrationDefinitions,
  useProjectIntegration,
} from '@omnara/react'
import { createOmnaraClient, type CronTrigger } from '@omnara/sdk'
import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { type FakeApi, fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId, integrationDefinition, projectIntegration } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, waitForUI } from '@/test/secret-editor'

const orgID = fakeId('org'),
  projectID = fakeId('proj')
const path = `/api/v1/orgs/${orgID}/projects/${projectID}`
const integration = projectIntegration()
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
  const query = useIntegrationDefinitions(orgID, projectID)
  return <div>{query.data?.data.map((definition) => definition.integration_type).join(',')}</div>
}
it('loads the registry through the project-scoped catalog hook', async () => {
  const api = fakeApi([
    {
      method: 'GET',
      path: path + '/integration-definitions',
      respond: () => Response.json({ data: [integrationDefinition()] }),
    },
  ])
  render(api, <Catalog />)
  await waitForUI(() => {
    expect(container.textContent).toContain('slack_thread')
  })
  expect(api.requestsTo('GET', path + '/integration-definitions')).toHaveLength(1)
})
function GitHubRegistration() {
  const detail = useProjectIntegration(orgID, projectID, integration.id)
  const start = useCreateProjectIntegrationGitHubSetup(orgID, projectID)
  return (
    <>
      <output>{detail.data?.setup_revision}</output>
      <button
        disabled={!detail.data || start.isPending}
        onClick={() => {
          if (detail.data)
            start.mutate({
              integrationID: integration.id,
              expected_setup_revision: detail.data.setup_revision,
            })
        }}
      >
        Register
      </button>
    </>
  )
}

it('refreshes a conflicted GitHub registration before retrying its setup revision', async () => {
  cache.setDefaultOptions({ queries: { retry: false, staleTime: 30_000 } })
  let current = projectIntegration({ integration_type: 'github_pr' }),
    attempts = 0
  const setupPath = path + '/integrations/' + integration.id + '/github-setup'
  const api = fakeApi([
    {
      method: 'GET',
      path: path + '/integrations/' + integration.id,
      respond: () => Response.json(current),
    },
    {
      method: 'POST',
      path: setupPath,
      respond: () => {
        if (++attempts === 1) {
          current = { ...current, setup_revision: 2 }
          return jsonResponse({ code: 'conflict', error: 'Integration setup changed' }, 409)
        }
        return Response.json({
          integration_id: integration.id,
          setup_revision: 2,
          registration_url: 'https://github.com/settings/apps/new',
          manifest: {},
          expires_at: '2026-09-22T01:00:00Z',
        })
      },
    },
  ])
  render(api, <GitHubRegistration />)
  await waitForUI(() => {
    expect(container.querySelector('output')?.textContent).toBe('1')
  })
  act(() => {
    button('Register').click()
  })
  await waitForUI(() => {
    expect(container.querySelector('output')?.textContent).toBe('2')
    expect(button('Register').disabled).toBe(false)
  })
  act(() => {
    button('Register').click()
  })
  await waitForUI(() => {
    expect(api.requestsTo('POST', setupPath)).toHaveLength(2)
  })
  expect(api.requestsTo('POST', setupPath).map((request) => request.body)).toEqual([
    { expected_setup_revision: 1 },
    { expected_setup_revision: 2 },
  ])
})

function Setup() {
  const detail = useProjectIntegration(orgID, projectID, integration.id)
  const configure = useConfigureProjectIntegration(orgID, projectID)
  const disconnect = useDisconnectProjectIntegration(orgID, projectID)
  return (
    <div>
      <output>
        {detail.data?.state}/{detail.data?.setup_revision}
      </output>
      <button
        onClick={() => {
          configure.mutate({
            integrationID: integration.id,
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
          disconnect.mutate(integration.id)
        }}
      >
        Disconnect
      </button>
    </div>
  )
}
it('uses integration setup and disconnect responses to refresh the same detail cache', async () => {
  const api = fakeApi([
    {
      method: 'GET',
      path: path + '/integrations/' + integration.id,
      respond: () => Response.json(integration),
    },
    {
      method: 'POST',
      path: path + '/integrations/' + integration.id + '/setup',
      respond: () => Response.json({ ...integration, state: 'active', setup_revision: 2 }),
    },
    {
      method: 'POST',
      path: path + '/integrations/' + integration.id + '/disconnect',
      respond: () => Response.json({ ...integration, state: 'disconnected', setup_revision: 3 }),
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
  expect(
    api.requestsTo('POST', path + '/integrations/' + integration.id + '/setup')[0]?.body,
  ).toEqual({
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
  expect(api.requestsTo('GET', path + '/integrations/' + integration.id)).toHaveLength(1)
})

it('refreshes integration details on tab focus even within the 30-second freshness window', async () => {
  cache.setDefaultOptions({ queries: { retry: false, staleTime: 30_000 } })
  let current = integration
  const integrationPath = path + '/integrations/' + integration.id
  const api = fakeApi([
    { method: 'GET', path: integrationPath, respond: () => Response.json(current) },
  ])
  render(api, <Setup />)
  await waitForUI(() => {
    expect(container.querySelector('output')?.textContent).toBe('disconnected/1')
  })
  act(() => {
    focusManager.setFocused(false)
  })
  current = {
    ...integration,
    state: 'active',
    setup_revision: 2,
    provider_tenant_id: 'T123',
    provider_account_ref: 'A123',
  }
  expect(cache.getQueryCache().getAll()[0]?.isStale()).toBe(false)
  expect(api.requestsTo('GET', integrationPath)).toHaveLength(1)
  act(() => {
    focusManager.setFocused(true)
  })
  await waitForUI(() => {
    expect(container.querySelector('output')?.textContent).toBe('active/2')
  })
  expect(api.requestsTo('GET', integrationPath)).toHaveLength(2)
  expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
})

const otherProjectID = `proj_${'b'.repeat(26)}`
function IntegrationScheduleLists() {
  const profileSchedules = useCronTriggers(orgID, projectID, {
    filters: { agent_profile_id: fakeId('aprf') },
  })
  const integrationSchedules = useCronTriggers(orgID, projectID, {
    filters: { integration_id: integration.id },
  })
  const otherSchedules = useCronTriggers(orgID, otherProjectID)
  const remove = useDeleteProjectIntegration(orgID, projectID)
  return (
    <>
      {[
        { label: 'Profile schedules', query: profileSchedules },
        { label: 'Integration schedules', query: integrationSchedules },
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
          remove.mutate(integration.id)
        }}
        disabled={remove.isPending}
      >
        Delete integration
      </button>
      {remove.isSuccess && <p>Integration deleted</p>}
    </>
  )
}

it('refreshes integration and profile cron lists after integration deletion without invalidating another project', async () => {
  const scheduled: CronTrigger = {
    id: fakeId('cron'),
    org_id: orgID,
    project_id: projectID,
    name: 'integration-schedule',
    target: {
      type: 'integration',
      integration_id: integration.id,
      settings: {
        agent_profile_id: fakeId('aprf'),
        channel_id: 'C123',
        opening_message_template: 'Daily update',
        message_template: 'Write a report.',
      },
    },
    cron: '0 9 * * *',
    timezone: 'UTC',
    enabled: true,
    last_fired_at: null,
    next_fire_at: null,
    failure_report: null,
    last_run: null,
    created_at: integration.created_at,
    updated_at: integration.updated_at,
  }
  const ordinary: CronTrigger = {
    ...scheduled,
    id: `cron_${'b'.repeat(26)}`,
    name: 'ordinary-schedule',
    message_template: 'Write a report.',
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
          data: url.searchParams.has('integration_id')
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
      path: path + '/integrations/' + integration.id,
      respond: () => {
        deleted = true
        return new Response(null, { status: 204 })
      },
    },
  ])
  render(api, <IntegrationScheduleLists />)
  const names = (label: string) =>
    container.querySelector(`output[aria-label="${label}"]`)?.textContent
  await waitForUI(() => {
    expect(names('Profile schedules')).toBe('integration-schedule,ordinary-schedule')
    expect(names('Integration schedules')).toBe('integration-schedule')
    expect(names('Other project schedules')).toBe('other-project-schedule')
  })
  act(() => {
    button('Delete integration').click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('Integration deleted')
    expect(names('Profile schedules')).toBe('ordinary-schedule')
    expect(names('Integration schedules')).toBe('')
  })
  expect(api.requestsTo('DELETE', path + '/integrations/' + integration.id)).toHaveLength(1)
  const reads = api.requestsTo('GET', path + '/cron-triggers')
  expect(
    reads.filter((request) => request.url.searchParams.get('agent_profile_id') === fakeId('aprf')),
  ).toHaveLength(2)
  expect(
    reads.filter((request) => request.url.searchParams.get('integration_id') === integration.id),
  ).toHaveLength(2)
  expect(api.requestsTo('GET', otherPath)).toHaveLength(1)
  expect(names('Other project schedules')).toBe('other-project-schedule')
})
