/** @vitest-environment happy-dom */

import type { AgentProfile } from '@omnara/sdk'
import { getIntegrationQueryKey } from '@omnara/sdk/tanstack'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { BreadcrumbSlotContext } from '@/components/layout/breadcrumb-slot-context'
import { ActiveOrgContext } from '@/lib/active-org-context'
import { IntegrationDetail, IntegrationPage } from '@/routes/IntegrationPage'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { agentConfigModel, fakeId, integration as integrationFixture } from '@/test/fixtures'
import { renderIntegration } from '@/test/integration-render'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, waitForUI } from '@/test/secret-editor'

const orgId = fakeId('org'),
  projectId = fakeId('proj'),
  profileId = fakeId('aprf')
const projectPath = `/api/v1/orgs/${orgId}/projects/${projectId}`
let root: Root, container: HTMLDivElement, restore: () => void

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

it.each(['same setup', 'new revision', 'disconnected'] as const)(
  'keeps confirmed launcher changes after a failed refresh and ignores an older GET (%s)',
  async (scenario) => {
    const integration = integrationFixture({
      integration_kind: 'discord_thread',
      state: 'active',
      provider_tenant_id: '111',
      provider_account_ref: '222',
      provider_config: { public_key: 'ab'.repeat(32) },
      settings: {
        launcher: {
          profiles: [profileId],
        },
      },
    })
    const failure = {
      message: 'Discord Gateway closed: 4014',
      retry_at: '2026-09-23T12:00:00Z',
    }
    const detail = { ...integration, runtime_failure: failure }
    const updated = integrationFixture({
      ...integration,
      settings: {},
      updated_at: '2026-09-23T11:00:00Z',
      setup_revision:
        scenario === 'new revision' ? integration.setup_revision + 1 : integration.setup_revision,
      state: scenario === 'disconnected' ? 'disconnected' : 'active',
    })
    const integrationPath = `${projectPath}/integrations/${integration.id}`
    let reads = 0
    let release!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => {
      release = resolve
    })
    const profile: AgentProfile = {
      id: profileId,
      name: 'Support',
      org_id: orgId,
      project_id: projectId,
      current_generation: 1,
      current_config_id: fakeId('acfg'),
      current_config: {
        id: fakeId('acfg'),
        org_id: orgId,
        project_id: projectId,
        model: agentConfigModel(),
        effective_definition_hash: 'hash',
        compiled_definition: {
          instruction: 'Help with this integration.',
          model: { configured_model_id: fakeId('mdl') },
        },
        created_at: integration.created_at,
      },
      created_at: integration.created_at,
      updated_at: integration.updated_at,
    }
    const api = fakeApi([
      {
        method: 'GET',
        path: integrationPath,
        respond: () => {
          if (++reads === 1) return Response.json(detail)
          if (reads === 2) return pending
          return jsonResponse({ code: 'internal_error', error: 'Refresh unavailable' }, 500)
        },
      },
      { method: 'PUT', path: integrationPath, respond: () => Response.json(updated) },
      {
        method: 'GET',
        path: `${projectPath}/agent-profiles`,
        respond: () => Response.json({ data: [profile], next_cursor: null }),
      },
      {
        method: 'GET',
        path: `${projectPath}/agent-profiles/${profileId}`,
        respond: () => Response.json(profile),
      },
      ...['cron-triggers', `integrations/${integration.id}/subscriptions`].map((resource) => ({
        method: 'GET',
        path: `${projectPath}/${resource}`,
        respond: () => Response.json({ data: [], next_cursor: null }),
      })),
    ])
    const { cache, client } = renderIntegration(
      root,
      api,
      <IntegrationDetail
        orgId={orgId}
        projectId={projectId}
        integrationId={integration.id}
        canManage
      />,
    )
    const queryKey = getIntegrationQueryKey({
      path: { orgID: orgId, projectID: projectId, integrationID: integration.id },
      client,
    })
    await waitForUI(() => {
      expect(container.textContent).toContain(`Connection failed: ${failure.message}`)
    })
    act(() => {
      button('Edit').click()
    })
    await waitForUI(() => {
      expect(button('Remove Support')).toBeDefined()
    })
    act(() => {
      button('Remove Support').click()
    })
    let refresh!: Promise<void>
    act(() => {
      refresh = cache.invalidateQueries({ queryKey })
    })
    await waitForUI(() => {
      expect(api.requestsTo('GET', integrationPath)).toHaveLength(2)
    })
    act(() => {
      button('Save changes').click()
    })
    await waitForUI(() => {
      expect(button('Choose profiles')).toBeDefined()
      expect(container.textContent).toContain('Could not refresh this integration.')
    })
    expect(api.requestsTo('PUT', integrationPath)[0]?.body).toEqual({
      settings: {},
    })
    await act(async () => {
      release(Response.json(detail))
      await refresh
    })
    expect(api.requestsTo('GET', integrationPath)).toHaveLength(3)
    expect(cache.getQueryData(queryKey)).toEqual({
      ...updated,
      runtime_failure: scenario === 'same setup' ? failure : undefined,
    })
    act(() => {
      button('Choose profiles').click()
    })
    await waitForUI(() => {
      expect(container.textContent).toContain('0/16 selected')
      expect(container.querySelector('[aria-label="Remove Support"]')).toBeNull()
      expect(container.textContent.includes(`Connection failed: ${failure.message}`)).toBe(
        scenario === 'same setup',
      )
    })
  },
)

it('keeps the loaded integration breadcrumb and settings during a transient refresh failure', async () => {
  const integration = integrationFixture({
    name: 'engineering-bot',
    state: 'active',
    provider_tenant_id: 'T123',
    provider_account_ref: 'A123',
  })
  const integrationPath = `${projectPath}/integrations/${integration.id}`
  let failing = false
  const api = fakeApi([
    {
      method: 'GET',
      path: `/api/v1/orgs/${orgId}/projects`,
      respond: () =>
        Response.json({
          data: [
            {
              id: projectId,
              org_id: orgId,
              name: 'Engineering',
              created_at: integration.created_at,
              updated_at: integration.updated_at,
              access: {
                can_read: true,
                can_manage: false,
                can_manage_access: false,
                can_operate: false,
              },
            },
          ],
          next_cursor: null,
        }),
    },
    {
      method: 'GET',
      path: integrationPath,
      respond: () =>
        failing
          ? jsonResponse({ code: 'unavailable', error: 'Temporary failure' }, 503)
          : Response.json(integration),
    },
    ...[`${integrationPath}/subscriptions`, `${projectPath}/cron-triggers`].map((path) => ({
      method: 'GET',
      path,
      respond: () => Response.json({ data: [], next_cursor: null }),
    })),
  ])
  const rootRoute = createRootRoute({
    component: function PageHeader() {
      const [slot, setSlot] = useState<HTMLDivElement | null>(null)
      return (
        <>
          <header>
            <div ref={setSlot} />
          </header>
          <BreadcrumbSlotContext value={slot}>
            <Outlet />
          </BreadcrumbSlotContext>
        </>
      )
    },
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([
      createRoute({
        getParentRoute: () => rootRoute,
        path: '/projects/$projectId/integrations/$integrationId',
        component: IntegrationPage,
      }),
    ]),
    history: createMemoryHistory({
      initialEntries: [`/projects/${projectId}/integrations/${integration.id}`],
    }),
  })
  const { cache, client } = renderIntegration(
    root,
    api,
    <ActiveOrgContext
      value={{
        orgs: [],
        activeOrg: { id: orgId, name: 'Acme', role: 'owner', created_at: integration.created_at },
        setActiveOrgId: () => undefined,
      }}
    >
      <RouterProvider router={router} />
    </ActiveOrgContext>,
  )
  const breadcrumb = () => container.querySelector('header [aria-label="breadcrumb"]')
  await waitForUI(() => {
    expect(breadcrumb()?.querySelector('[aria-current="page"]')?.textContent).toBe(
      'engineering-bot',
    )
  })
  expect(
    breadcrumb()?.querySelector(`a[href="/projects/${projectId}/integrations"]`)?.textContent,
  ).toBe('Integrations')
  failing = true
  await act(async () => {
    await cache.invalidateQueries({
      queryKey: getIntegrationQueryKey({
        path: { orgID: orgId, projectID: projectId, integrationID: integration.id },
        client,
      }),
    })
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('Could not refresh this integration.')
  })
  expect(breadcrumb()?.querySelector('[aria-current="page"]')?.textContent).toBe('engineering-bot')
  expect(container.querySelector('h1')?.textContent).toBe('engineering-bot')
  failing = false
  act(() => {
    button('Retry refresh').click()
  })
  await waitForUI(() => {
    expect(container.textContent).not.toContain('Could not refresh this integration.')
  })
  expect(breadcrumb()?.querySelector('[aria-current="page"]')?.textContent).toBe('engineering-bot')
})
