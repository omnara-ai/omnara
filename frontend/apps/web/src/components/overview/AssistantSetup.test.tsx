/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import {
  type AgentPanel,
  createOmnaraClient,
  type ProjectModelGrantListItem,
  type VisibleProject,
} from '@omnara/sdk'
import { getCurrentUserQueryKey } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it } from 'vitest'

import { fakeApi, jsonResponse } from '@/test/fake-api'
import { currentUser, currentUserOrg, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

import { AssistantSetup } from './AssistantSetup'

const timestamp = '2026-01-01T00:00:00Z'
const org = currentUserOrg({ name: 'Acme', role: 'owner' })
const access = { can_read: true, can_manage: true, can_manage_access: true, can_operate: true }
const defaultProject: VisibleProject = {
  id: fakeId('proj'),
  org_id: org.id,
  name: 'Default',
  created_at: timestamp,
  updated_at: timestamp,
  access,
}
const prepared: VisibleProject = {
  ...defaultProject,
  id: `proj_${'b'.repeat(26)}`,
  name: 'Prepared',
  created_at: '2026-02-01T00:00:00Z',
}
const grant: ProjectModelGrantListItem = {
  grant: {
    id: fakeId('pmog'),
    org_id: org.id,
    project_id: prepared.id,
    configured_model_id: fakeId('mdl'),
    supported_reasoning_efforts: [],
    input_modalities: [],
    output_modalities: [],
    created_at: timestamp,
    updated_at: timestamp,
  },
  model: {
    id: fakeId('mdl'),
    org_id: org.id,
    model_provider_config_id: fakeId('mpc'),
    name: 'tool-model',
    provider_config: 'openrouter',
    provider_model_slug: 'tool-model',
    created_at: timestamp,
    updated_at: timestamp,
  },
  effective_reasoning: {
    supports_reasoning: false,
    default_reasoning_effort: '',
    supported_reasoning_efforts: [],
  },
  supports_tools: true,
}

it('lets a project that already has a model be chosen when the first one has none', async () => {
  const restore = enableReactActEnvironment()
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  const panel: AgentPanel = { revision: 0, state: 'empty' }
  const createPath = `/api/v1/orgs/${org.id}/assistant-agents`
  const api = fakeApi([
    {
      method: 'GET',
      path: `/api/v1/orgs/${org.id}/projects`,
      respond: () => jsonResponse({ data: [defaultProject, prepared], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `/api/v1/orgs/${org.id}/projects/${defaultProject.id}/model-grants`,
      respond: () => jsonResponse({ data: [], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `/api/v1/orgs/${org.id}/projects/${prepared.id}/model-grants`,
      respond: () => jsonResponse({ data: [grant], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `/api/v1/orgs/${org.id}/model-providers`,
      respond: () => jsonResponse({ data: [], next_cursor: null }),
    },
    {
      method: 'POST',
      path: createPath,
      respond: () => jsonResponse({ agent_id: fakeId('agt'), panel }),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', maxRetries: 0 })
  client.setConfig({ fetch: api.fetch })
  const cache = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  cache.setQueryData(getCurrentUserQueryKey({ client }), currentUser([org]))
  const button = (label: string) =>
    [...container.querySelectorAll('button')].find((item) => item.textContent.trim() === label)
  const press = async (element: Element | null | undefined) => {
    if (!(element instanceof HTMLElement)) throw new Error('Missing clickable element')
    await act(async () => {
      element.click()
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
  }
  try {
    await act(async () => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>
            <AssistantSetup org={org} panel={panel} />
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    // The oldest project is prefilled and has no model, and the other can still be chosen.
    await waitForUI(() => {
      expect(button('Add a model')).toBeDefined()
    })
    await press(document.getElementById('assistant-setup-project'))
    const option = () =>
      [...document.querySelectorAll('[role="option"]')].find(
        (item) => item.textContent.trim() === prepared.name,
      )
    await waitForUI(() => {
      expect(option()).toBeDefined()
    })
    await press(option())
    await waitForUI(() => {
      expect(button('Create assistant')?.disabled).toBe(false)
    })
    await press(button('Create assistant'))
    await waitForUI(() => {
      expect(api.requestsTo('POST', createPath).map(({ body }) => body)).toMatchObject([
        { project_id: prepared.id, model_id: grant.model.id, automatic: false },
      ])
    })
  } finally {
    act(() => {
      root.unmount()
    })
    cache.clear()
    container.remove()
    restore()
  }
})
