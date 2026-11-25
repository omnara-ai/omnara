/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { type AgentConfig, createOmnaraClient, type CurrentUserOrg } from '@omnara/sdk'
import { zCreateAgentProfileRequest } from '@omnara/sdk/zod'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { agentTemplates } from '@/components/agents/agentTemplates'
import { CreateAgentFormView } from '@/components/agents/CreateAgentFormView'
import { savePendingMcpBuilderOAuth } from '@/components/agents/pendingMcpBuilderOAuth'
import { emptyBasicConfig } from '@/components/agents/useAgentBuilderForm'
import { ActiveOrgContext } from '@/lib/active-org-context'
import { type FakeApi, fakeApi, jsonResponse } from '@/test/fake-api'
import { agentConfigModel, currentUserOrg, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, field, waitForUI } from '@/test/secret-editor'

const timestamp = '2026-11-01T00:00:00Z'
const orgId = fakeId('org')
const projectId = fakeId('proj')
const projectPath = `/api/v1/orgs/${orgId}/projects/${projectId}`
const config: AgentConfig = {
  id: fakeId('acfg'),
  org_id: orgId,
  project_id: projectId,
  model: agentConfigModel(),
  effective_definition_hash: 'hash',
  compiled_definition: {
    instruction: 'Help.',
    model: { configured_model_id: fakeId('mdl') },
  },
  created_at: timestamp,
}

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

function builderApi(profileReady: Promise<void> = Promise.resolve()) {
  return fakeApi([
    {
      method: 'GET',
      path: `/api/v1/orgs/${orgId}/projects`,
      respond: () =>
        jsonResponse({
          data: [
            {
              id: projectId,
              org_id: orgId,
              name: 'Alpha',
              access: {
                can_read: true,
                can_manage: true,
                can_manage_access: true,
                can_operate: true,
              },
              created_at: timestamp,
              updated_at: timestamp,
            },
          ],
          next_cursor: null,
        }),
    },
    { method: 'POST', path: `${projectPath}/agent-configs`, respond: () => Response.json(config) },
    {
      method: 'POST',
      path: `${projectPath}/agent-profiles`,
      respond: async () => {
        await profileReady
        return Response.json({
          id: fakeId('aprf'),
          org_id: orgId,
          project_id: projectId,
          name: 'General agent',
          current_config_id: config.id,
          current_generation: 1,
          current_config: config,
          created_at: timestamp,
          updated_at: timestamp,
        })
      },
    },
  ])
}

async function render(api: FakeApi, activeOrg: CurrentUserOrg) {
  const rootRoute = createRootRoute()
  const router = createRouter({
    routeTree: rootRoute.addChildren([
      createRoute({
        getParentRoute: () => rootRoute,
        path: '/projects/$projectId/agents/new',
        component: () => (
          <CreateAgentFormView
            templatesReady
            initialTemplate={agentTemplates[0]}
            defaultModel={{
              id: fakeId('mdl'),
              org_id: orgId,
              model_provider_config_id: fakeId('mpc'),
              name: 'gpt',
              provider_config: 'openai',
              provider_model_slug: 'gpt',
              created_at: timestamp,
              updated_at: timestamp,
            }}
          />
        ),
      }),
      createRoute({
        getParentRoute: () => rootRoute,
        path: '/projects/$projectId/agent-profiles/$profileId',
        component: () => <p>Profile page</p>,
      }),
    ]),
    history: createMemoryHistory({ initialEntries: [`/projects/${projectId}/agents/new`] }),
  })
  const client = createOmnaraClient({
    baseUrl: 'https://omnara.test/api/v1',
    fetch: api.fetch,
    maxRetries: 0,
  })
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  cache.setQueryDefaults(['web-config'], { enabled: false })
  cache.setQueryData(['web-config'], {})
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={cache}>
          <ActiveOrgContext.Provider
            value={{ orgs: [activeOrg], activeOrg, setActiveOrgId: () => undefined }}
          >
            <RouterProvider router={router} />
          </ActiveOrgContext.Provider>
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  await waitForUI(() => {
    expect(container.querySelector('#agent-config-name')).not.toBeNull()
  })
}

function budgetLabels() {
  return [...container.querySelectorAll('label')].filter(
    (label) => label.textContent === 'Budget (USD)',
  )
}

async function createProfile(api: FakeApi) {
  await waitForUI(() => {
    expect(button('Create profile').disabled).toBe(false)
  })
  act(() => {
    button('Create profile').click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('Profile page')
  })
  const [request] = api.requestsTo('POST', `${projectPath}/agent-profiles`)
  return zCreateAgentProfileRequest.parse(request?.body)
}

it('creates the profile with the budget an org admin enters', async () => {
  const api = builderApi()
  await render(api, currentUserOrg({ id: orgId, role: 'admin' }))

  await enter('Budget (USD)', '0')
  expect(container.textContent).toContain('Enter an amount above 0 in US dollars.')
  act(() => {
    button('Create profile').click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('Enter a budget amount above 0 in US dollars.')
  })
  expect(api.requestsTo('POST', `${projectPath}/agent-profiles`)).toHaveLength(0)

  await enter('Budget (USD)', '25')
  expect(await createProfile(api)).toEqual({
    name: 'General agent',
    config: config.id,
    budgets: [{ interval: 'month', amount_usd: '25' }],
  })
})

it('creates the profile without budgets when the admin leaves the budget empty', async () => {
  const api = builderApi()
  await render(api, currentUserOrg({ id: orgId, role: 'owner' }))

  expect(await createProfile(api)).toEqual({
    name: 'General agent',
    config: config.id,
    budgets: [],
  })
})

it('hides the budget from developers', async () => {
  const api = builderApi()
  await render(api, currentUserOrg({ id: orgId, role: 'developer' }))

  expect(budgetLabels()).toHaveLength(0)
  expect(await createProfile(api)).toEqual({
    name: 'General agent',
    config: config.id,
    budgets: [],
  })
})

it('locks the budget while its profile is created and after', async () => {
  let releaseProfile: () => void = () => undefined
  const api = builderApi(
    new Promise<void>((resolve) => {
      releaseProfile = resolve
    }),
  )
  await render(api, currentUserOrg({ id: orgId, role: 'admin' }))
  await enter('Budget (USD)', '25')
  await waitForUI(() => {
    expect(button('Create & launch agent').disabled).toBe(false)
  })
  act(() => {
    button('Create & launch agent').click()
  })

  await waitForUI(() => {
    expect(field('Budget (USD)').disabled).toBe(true)
  })
  expect(container.textContent).not.toContain('This profile already exists.')
  releaseProfile()
  await waitForUI(() => {
    expect(container.textContent).toContain(
      'This profile already exists. Change its budget on its Usage tab.',
    )
  })
  expect(field('Budget (USD)').disabled).toBe(true)
  expect(api.requestsTo('POST', `${projectPath}/agent-profiles`)).toHaveLength(1)
})

it('shows the budget in YAML mode', async () => {
  const api = builderApi()
  await render(api, currentUserOrg({ id: orgId, role: 'admin' }))
  await enter('Budget (USD)', '25')
  act(() => {
    button('YAML').click()
  })

  await waitForUI(() => {
    const visible = budgetLabels().filter((label) => !label.closest('.hidden'))
    expect(visible).toHaveLength(1)
    expect(document.getElementById(visible[0]?.htmlFor ?? '')).toHaveProperty('value', '25')
  })
})

it('keeps the budget through an MCP sign-in', async () => {
  const path = `/projects/${projectId}/agents/new`
  savePendingMcpBuilderOAuth({
    returnPath: path,
    serverId: 'server',
    agentName: 'General agent',
    budget: { interval: 'week', amount: '40' },
    draft: emptyBasicConfig,
  })
  window.history.replaceState(null, '', `${path}?mcp_oauth=success&secret_id=sec_a`)
  try {
    await render(builderApi(), currentUserOrg({ id: orgId, role: 'admin' }))

    const amount = field('Budget (USD)')
    expect(amount.value).toBe('40')
    expect(document.getElementById(`${amount.id}-interval`)?.textContent).toBe('Weekly')
  } finally {
    window.history.replaceState(null, '', '/')
  }
})
