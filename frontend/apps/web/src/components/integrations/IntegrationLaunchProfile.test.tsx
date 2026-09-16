/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { type AgentProfile, createOmnaraClient, type IntegrationInstall } from '@omnara/sdk'
import {
  getIntegrationLaunchProfileQueryKey,
  listIntegrationInstallsQueryKey,
} from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { fakeApi, type FakeRoute, jsonResponse } from '@/test/fake-api'
import { agentConfigModel, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, submit, waitForUI } from '@/test/secret-editor'

import { ConnectIntegrationAppDialog } from './ConnectIntegrationAppDialog'
import { IntegrationLaunchProfileDialog } from './IntegrationLaunchProfileDialog'
import { ProjectIntegrationsSection } from './ProjectIntegrationsSection'

const orgId = fakeId('org')
const projectId = fakeId('proj')
const now = '2026-09-15T00:00:00Z'
const base = `/api/v1/orgs/${orgId}/projects/${projectId}`
const install: IntegrationInstall = {
  id: fakeId('iin'),
  org_id: orgId,
  project_id: projectId,
  integration_app_id: fakeId('iapp'),
  provider: 'github',
  integration_kind: 'managed',
  connection_mode: 'repository',
  state: 'active',
  display_name: 'team/repo',
  metadata: {},
  created_at: now,
  updated_at: now,
}
const profile: AgentProfile = {
  id: fakeId('aprf'),
  org_id: orgId,
  project_id: projectId,
  name: 'inbound-helper',
  current_config_id: fakeId('acfg'),
  current_generation: 1,
  current_config: {
    id: fakeId('acfg'),
    org_id: orgId,
    project_id: projectId,
    effective_definition_hash: 'hash',
    model: agentConfigModel(),
    created_at: now,
  },
  created_at: now,
  updated_at: now,
}
const launchPath = `${base}/integration-installs/${install.id}/launch-profile`
let root: Root
let container: HTMLDivElement
let restore: () => void
beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  restore()
})

async function render(ui: ReactNode, routes: FakeRoute[] = []) {
  const api = fakeApi([
    ...routes,
    { method: 'GET', path: launchPath, respond: () => jsonResponse({ agent_profile_id: null }) },
    {
      method: 'GET',
      path: base + '/agent-profiles',
      respond: () => jsonResponse({ data: [profile], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `${base}/agent-profiles/${profile.id}`,
      respond: () => jsonResponse(profile),
    },
    {
      method: 'GET',
      path: base + '/integration-apps',
      respond: () => jsonResponse({ data: [], next_cursor: null }),
    },
    {
      method: 'GET',
      path: base + '/integration-installs',
      respond: () => jsonResponse({ data: [{ ...install, metadata: {} }], next_cursor: null }),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await Promise.resolve()
  })
  return { api, queryClient, client }
}

function dialog(canManage = true, onClose = vi.fn()) {
  return (
    <IntegrationLaunchProfileDialog
      orgId={orgId}
      projectId={projectId}
      install={install}
      canManage={canManage}
      onClose={onClose}
    />
  )
}

async function chooseProfile() {
  await waitForUI(() => {
    expect(document.getElementById('connection-inbound-profile')).not.toBeNull()
  })
  act(() => {
    document.getElementById('connection-inbound-profile')?.click()
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="option"]')?.textContent).toBe(profile.name)
  })
  act(() => {
    document.querySelector<HTMLElement>('[role="option"]')?.click()
  })
}

it.each([null, profile.id])(
  'lets ProjectRead view the saved profile %s without editing or fetching choices',
  async (profileId) => {
    const { api } = await render(
      <ProjectIntegrationsSection orgId={orgId} projectId={projectId} canManage={false} />,
      [
        {
          method: 'GET',
          path: launchPath,
          respond: () => jsonResponse({ agent_profile_id: profileId }),
        },
      ],
    )
    await waitForUI(() => {
      expect(button('Inbound profile')).toBeDefined()
    })
    act(() => {
      button('Inbound profile').click()
    })
    await waitForUI(() => {
      expect(document.body.textContent).toContain(
        profileId ? profile.name : 'New launches disabled',
      )
    })
    expect(document.querySelector('[role="combobox"]')).toBeNull()
    expect(
      [...document.querySelectorAll('button')].some((item) => item.textContent === 'Save'),
    ).toBe(false)
    expect(document.body.textContent).toContain(
      'Existing conversations keep their agents and replies',
    )
    await submit()
    expect(api.requestsTo('PUT', launchPath)).toHaveLength(0)
    expect(api.requestsTo('GET', base + '/agent-profiles')).toHaveLength(0)
    expect(api.requests.every((request) => request.url.pathname.startsWith(base))).toBe(true)
  },
)

it('saves a selected profile with the required property and refreshes the connection cache without creating an agent', async () => {
  const close = vi.fn()
  const { api, client, queryClient } = await render(dialog(true, close), [
    {
      method: 'PUT',
      path: launchPath,
      respond: () => jsonResponse({ agent_profile_id: profile.id }),
    },
  ])
  const connectionsKey = listIntegrationInstallsQueryKey({
    path: { orgID: orgId, projectID: projectId },
    client,
  })
  queryClient.setQueryData(connectionsKey, { data: [install], next_cursor: null })
  await chooseProfile()
  await submit()
  await waitForUI(() => {
    expect(close).toHaveBeenCalledOnce()
  })
  expect(api.requestsTo('PUT', launchPath).map((request) => request.body)).toEqual([
    { agent_profile_id: profile.id },
  ])
  expect(
    queryClient.getQueryData(
      getIntegrationLaunchProfileQueryKey({
        path: { orgID: orgId, projectID: projectId, integrationInstallID: install.id },
        client,
      }),
    ),
  ).toEqual({ agent_profile_id: profile.id })
  expect(queryClient.getQueryState(connectionsKey)?.isInvalidated).toBe(true)
  expect(api.requests.filter((request) => request.method !== 'GET')).toHaveLength(1)
})

it('loads a saved profile outside the first choices page and explicitly clears it with null', async () => {
  let finish: (response: Response) => void = () => {
    throw new Error('PUT has not started')
  }
  const close = vi.fn()
  const { api } = await render(dialog(true, close), [
    {
      method: 'GET',
      path: launchPath,
      respond: () => jsonResponse({ agent_profile_id: profile.id }),
    },
    {
      method: 'GET',
      path: base + '/agent-profiles',
      respond: () => jsonResponse({ data: [], next_cursor: 'next' }),
    },
    {
      method: 'PUT',
      path: launchPath,
      respond: () =>
        new Promise<Response>((resolve) => {
          finish = resolve
        }),
    },
  ])
  await waitForUI(() => {
    expect(button(`Clear ${profile.name}`)).toBeDefined()
  })
  expect(button('Save').disabled).toBe(true)
  act(() => {
    button(`Clear ${profile.name}`).click()
  })
  expect(document.body.textContent).toContain(
    'Without a profile, incoming activity cannot launch new agents',
  )
  act(() => {
    document
      .querySelector('form')
      ?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  })
  await waitForUI(() => {
    expect(api.requestsTo('PUT', launchPath)).toHaveLength(1)
    expect(button('Save').disabled).toBe(true)
    expect(button('Cancel').disabled).toBe(true)
  })
  act(() => {
    document
      .querySelector('form')
      ?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  })
  expect(api.requestsTo('PUT', launchPath).map((request) => request.body)).toEqual([
    { agent_profile_id: null },
  ])
  await act(async () => {
    finish(jsonResponse({ agent_profile_id: null }))
    await Promise.resolve()
  })
  await waitForUI(() => {
    expect(close).toHaveBeenCalledOnce()
  })
  expect(api.requests.filter((request) => request.method !== 'GET')).toHaveLength(1)
})

it('retains the selected profile after a save failure and allows retry', async () => {
  let attempts = 0
  const close = vi.fn()
  const { api } = await render(dialog(true, close), [
    {
      method: 'PUT',
      path: launchPath,
      respond: () =>
        ++attempts === 1
          ? jsonResponse({ code: 'conflict', error: 'Connection changed. Try again.' }, 409)
          : jsonResponse({ agent_profile_id: profile.id }),
    },
  ])
  await chooseProfile()
  await submit()
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Connection changed. Try again.')
  })
  expect(document.getElementById('connection-inbound-profile')?.textContent).toBe(profile.name)
  expect(close).not.toHaveBeenCalled()
  await submit()
  await waitForUI(() => {
    expect(close).toHaveBeenCalledOnce()
  })
  expect(api.requestsTo('PUT', launchPath).map((request) => request.body)).toEqual([
    { agent_profile_id: profile.id },
    { agent_profile_id: profile.id },
  ])
})

it('does not assume null after a load failure and permits retry without writing', async () => {
  let attempts = 0
  const { api } = await render(dialog(), [
    {
      method: 'GET',
      path: launchPath,
      respond: () =>
        ++attempts === 1
          ? jsonResponse({ code: 'service_unavailable', error: 'Temporarily unavailable' }, 503)
          : jsonResponse({ agent_profile_id: null }),
    },
  ])
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Temporarily unavailable')
  })
  expect(document.querySelector('form')).toBeNull()
  act(() => {
    button('Retry').click()
  })
  await waitForUI(() => {
    expect(document.body.textContent).toContain('New launches disabled')
  })
  expect(button('Save').disabled).toBe(true)
  expect(api.requestsTo('PUT', launchPath)).toHaveLength(0)
})

it('keeps the current profile ID if its label is unavailable instead of silently clearing it', async () => {
  const { api } = await render(dialog(), [
    {
      method: 'GET',
      path: launchPath,
      respond: () => jsonResponse({ agent_profile_id: profile.id }),
    },
    {
      method: 'GET',
      path: base + '/agent-profiles',
      respond: () => jsonResponse({ data: [], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `${base}/agent-profiles/${profile.id}`,
      respond: () => jsonResponse({ code: 'not_found', error: 'Profile missing' }, 404),
    },
  ])
  await waitForUI(() => {
    expect(button('Clear Unavailable profile')).toBeDefined()
  })
  expect(button('Save').disabled).toBe(true)
  await submit()
  expect(api.requestsTo('PUT', launchPath)).toHaveLength(0)
})

it.each(['connection', 'setup'])(
  'searches profiles beyond the first page in the %s picker',
  async (view) => {
    const triggerId = view === 'connection' ? 'connection-inbound-profile' : 'inbound-profile'
    const { api } = await render(
      view === 'connection' ? (
        dialog()
      ) : (
        <ConnectIntegrationAppDialog
          orgId={orgId}
          projectId={projectId}
          app={{
            id: fakeId('iapp'),
            org_id: orgId,
            name: 'team-app',
            provider: 'github',
            provider_app_ref: '123',
            state: 'active',
            created_at: now,
            updated_at: now,
          }}
          onClose={vi.fn()}
        />
      ),
      [
        {
          method: 'GET',
          path: base + '/agent-profiles',
          respond: (request) =>
            jsonResponse(
              request.url.searchParams.get('name') === '*inbound-helper*'
                ? { data: [profile], next_cursor: null }
                : { data: [], next_cursor: 'more-profiles' },
            ),
        },
      ],
    )
    await waitForUI(() => {
      expect(document.getElementById(triggerId)).not.toBeNull()
    })
    act(() => {
      document.getElementById(triggerId)?.click()
    })
    await enter('Search profiles…', profile.name)
    await waitForUI(() => {
      expect(document.querySelector('[role="option"]')?.textContent).toBe(profile.name)
    })
    act(() => {
      document.querySelector<HTMLElement>('[role="option"]')?.click()
    })
    expect(document.getElementById(triggerId)?.textContent).toBe(profile.name)
    expect(
      api
        .requestsTo('GET', base + '/agent-profiles')
        .some(
          (request) =>
            request.url.searchParams.get('name') === '*inbound-helper*' &&
            request.url.searchParams.get('sort') === 'name',
        ),
    ).toBe(true)
    expect(api.requests.filter((request) => request.method !== 'GET')).toHaveLength(0)
  },
)

it.each([
  { integration_kind: 'external' as const, provider: 'github' },
  { integration_kind: 'managed' as const, provider: 'custom' },
])('omits inbound settings for unsupported connections: %s', async (overrides) => {
  const { api } = await render(
    <ProjectIntegrationsSection orgId={orgId} projectId={projectId} canManage />,
    [
      {
        method: 'GET',
        path: base + '/integration-installs',
        respond: () =>
          jsonResponse({ data: [{ ...install, ...overrides, metadata: {} }], next_cursor: null }),
      },
    ],
  )
  await waitForUI(() => {
    expect(document.body.textContent).toContain(install.display_name)
  })
  expect(document.body.textContent).not.toContain('Inbound profile')
  expect(api.requestsTo('GET', launchPath)).toHaveLength(0)
})
