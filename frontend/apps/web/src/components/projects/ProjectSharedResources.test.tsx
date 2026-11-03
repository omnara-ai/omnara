/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import {
  createOmnaraClient,
  type CurrentUserOrg,
  type ProjectAccess,
  type ProjectMachineGrantListItem,
  type ProjectModelGrantListItem,
  type ProjectSkillAccess,
  type VisibleProject,
} from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { act, createContext, type ReactNode, useContext } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'

import { ProjectSharedSkills } from '@/components/projects/ProjectSharedSkills'
import { ActiveOrgContext } from '@/lib/active-org-context'
import { ProjectMachinesPage } from '@/routes/ProjectMachinesPage'
import { ProjectModelsPage } from '@/routes/ProjectModelsPage'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { currentUserOrg, fakeId, machinePool, projectMachinePoolGrant } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'

const timestamp = '2026-01-01T00:00:00Z'
const orgId = fakeId('org')
const projectId = fakeId('proj')

const readerAccess: ProjectAccess = {
  can_read: true,
  can_manage: false,
  can_manage_access: false,
  can_operate: false,
}
const developerAccess: ProjectAccess = { ...readerAccess, can_manage: true, can_operate: true }
const adminAccess: ProjectAccess = { ...developerAccess, can_manage_access: true }

const modelGrant: ProjectModelGrantListItem = {
  grant: {
    id: fakeId('pmog'),
    org_id: orgId,
    project_id: projectId,
    configured_model_id: fakeId('mdl'),
    supported_reasoning_efforts: [],
    input_modalities: [],
    output_modalities: [],
    created_at: timestamp,
    updated_at: timestamp,
  },
  model: {
    id: fakeId('mdl'),
    org_id: orgId,
    model_provider_config_id: fakeId('mpc'),
    name: 'shared-model',
    provider_config: 'openai',
    provider_model_slug: 'shared-model',
    created_at: timestamp,
    updated_at: timestamp,
  },
  effective_reasoning: {
    supports_reasoning: false,
    default_reasoning_effort: '',
    supported_reasoning_efforts: [],
  },
}

const pool = machinePool({ name: 'shared-pool' })
const poolGrant = {
  grant: projectMachinePoolGrant({ machine_pool_id: pool.id, project_id: projectId }),
  machine_pool: {
    id: pool.id,
    org_id: orgId,
    name: pool.name,
    management_kind: pool.management_kind,
    description: '',
    provider: pool.provider,
    created_at: timestamp,
    updated_at: timestamp,
  },
}

const machineGrant: ProjectMachineGrantListItem = {
  grant: {
    id: fakeId('pmg'),
    org_id: orgId,
    project_id: projectId,
    machine_id: fakeId('mch'),
    source_kind: 'explicit',
    description: '',
    metadata: {},
    created_at: timestamp,
    updated_at: timestamp,
  },
  machine: {
    id: fakeId('mch'),
    org_id: orgId,
    source_kind: 'byo',
    display_name: 'shared-machine',
    description: '',
    provider: 'byo',
    lifecycle_state: 'active',
    connection_state: 'online',
    last_observed_at: null,
    deleted_at: null,
    created_at: timestamp,
    updated_at: timestamp,
  },
}

const sharedSkill: ProjectSkillAccess = {
  project_id: projectId,
  skill: {
    id: fakeId('skl'),
    org_id: orgId,
    owner: { kind: 'org' },
    name: 'shared-skill',
    revision_id: fakeId('skr'),
    revision: 1,
    description: 'A shared skill',
    created_at: timestamp,
    updated_at: timestamp,
  },
  availability: { source: 'grant', grant_id: fakeId('skg') },
}

const TestContentContext = createContext<ReactNode>(null)
function TestContent() {
  return useContext(TestContentContext)
}

function testRouter(path: string) {
  const rootRoute = createRootRoute({ component: Outlet })
  return createRouter({
    routeTree: rootRoute.addChildren([
      createRoute({
        getParentRoute: () => rootRoute,
        path: '/projects/$projectId/models',
        component: ProjectModelsPage,
      }),
      createRoute({
        getParentRoute: () => rootRoute,
        path: '/projects/$projectId/machines',
        component: ProjectMachinesPage,
      }),
      createRoute({ getParentRoute: () => rootRoute, path: '/content', component: TestContent }),
    ]),
    history: createMemoryHistory({ initialEntries: [path] }),
  })
}

let container: HTMLDivElement
let root: Root
let restoreActEnvironment: () => void

beforeAll(() => {
  restoreActEnvironment = enableReactActEnvironment()
})

afterAll(() => {
  restoreActEnvironment()
})

beforeEach(() => {
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
})

async function renderAt(
  path: string,
  {
    access,
    role,
    content,
  }: { access: ProjectAccess; role: CurrentUserOrg['role']; content?: ReactNode },
) {
  const project: VisibleProject = {
    id: projectId,
    org_id: orgId,
    name: 'Shared project',
    created_at: timestamp,
    updated_at: timestamp,
    access,
  }
  const projectPath = `/api/v1/orgs/${orgId}/projects/${projectId}`
  const api = fakeApi([
    {
      method: 'GET',
      path: `/api/v1/orgs/${orgId}/projects`,
      respond: () => jsonResponse({ data: [project], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `${projectPath}/model-grants`,
      respond: () => jsonResponse({ data: [modelGrant], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `${projectPath}/machine-pool-grants`,
      respond: () =>
        new Response(JSON.stringify({ data: [poolGrant], next_cursor: null }), {
          headers: { 'Content-Type': 'application/json' },
        }),
    },
    {
      method: 'GET',
      path: `${projectPath}/machine-grants`,
      respond: () => jsonResponse({ data: [machineGrant], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `${projectPath}/skills`,
      respond: () => jsonResponse({ data: [sharedSkill], next_cursor: null }),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const activeOrg = currentUserOrg({ id: orgId, role })
  const router = testRouter(path)
  await act(async () => {
    await router.load()
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <ActiveOrgContext.Provider
            value={{ orgs: [activeOrg], activeOrg, setActiveOrgId: () => undefined }}
          >
            <TestContentContext value={content}>
              <RouterProvider router={router} />
            </TestContentContext>
          </ActiveOrgContext.Provider>
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })
}

function buttonsLabelled(label: string) {
  return container.querySelectorAll(`button[aria-label="${label}"]`)
}

function hasButtonText(text: string) {
  return [...container.querySelectorAll('button')].some((button) => button.textContent === text)
}

it('shows shared models to project readers without grant actions', async () => {
  await renderAt(`/projects/${projectId}/models`, { access: readerAccess, role: 'member' })

  await vi.waitFor(() => {
    expect(container.textContent).toContain('shared-model')
  })
  expect(container.textContent).not.toContain('permission')
  expect(buttonsLabelled('Row actions')).toHaveLength(0)
  expect(hasButtonText('Share models')).toBe(false)
})

it('lets project access managers edit and stop sharing models', async () => {
  await renderAt(`/projects/${projectId}/models`, { access: adminAccess, role: 'member' })

  await vi.waitFor(() => {
    expect(container.textContent).toContain('shared-model')
  })
  expect(buttonsLabelled('Row actions')).toHaveLength(1)
  expect(hasButtonText('Share models')).toBe(true)
})

it('blocks the models page without project read access', async () => {
  await renderAt(`/projects/${projectId}/models`, {
    access: { ...readerAccess, can_read: false },
    role: 'member',
  })

  await vi.waitFor(() => {
    expect(container.textContent).toContain(
      'You don’t have permission to view shared models in this project.',
    )
  })
})

it('shows shared pools and machines to project developers without grant actions', async () => {
  await renderAt(`/projects/${projectId}/machines`, { access: developerAccess, role: 'member' })

  await vi.waitFor(() => {
    expect(container.textContent).toContain('shared-pool')
    expect(container.textContent).toContain('shared-machine')
  })
  expect(buttonsLabelled('Row actions')).toHaveLength(0)
  expect(hasButtonText('Share pool')).toBe(false)
  expect(hasButtonText('Share machines')).toBe(false)
})

it('requires org management to change machine grants', async () => {
  await renderAt(`/projects/${projectId}/machines`, { access: adminAccess, role: 'member' })

  await vi.waitFor(() => {
    expect(container.textContent).toContain('shared-pool')
    expect(container.textContent).toContain('shared-machine')
  })
  expect(buttonsLabelled('Row actions')).toHaveLength(0)
  expect(hasButtonText('Share pool')).toBe(false)
})

it('lets org admins with project access edit and stop sharing machine grants', async () => {
  await renderAt(`/projects/${projectId}/machines`, { access: adminAccess, role: 'admin' })

  // BYO machines are shared from their own page, so only the pool has row actions here.
  await vi.waitFor(() => {
    expect(buttonsLabelled('Row actions')).toHaveLength(1)
  })
  expect(hasButtonText('Share pool')).toBe(true)
})

it.each([
  { canManage: false, actions: 0 },
  { canManage: true, actions: 1 },
])(
  'shows stop sharing for shared skills when canManage is $canManage',
  async ({ canManage, actions }) => {
    await renderAt('/content', {
      access: readerAccess,
      role: 'member',
      content: (
        <ProjectSharedSkills
          orgId={orgId}
          projectId={projectId}
          projectName="Shared project"
          canManage={canManage}
        />
      ),
    })

    await vi.waitFor(() => {
      expect(container.textContent).toContain('shared-skill')
    })
    expect(buttonsLabelled('Skill actions')).toHaveLength(actions)
  },
)
