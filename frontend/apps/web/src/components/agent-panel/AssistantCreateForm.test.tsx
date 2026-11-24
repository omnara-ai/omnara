/** @vitest-environment happy-dom */

import { OmnaraClientProvider, useAgentPanel, useCreateAssistantAgent } from '@omnara/react'
import {
  type AgentPanel,
  createOmnaraClient,
  type ProjectModelGrantListItem,
  type VisibleProject,
} from '@omnara/sdk'
import { getAgentPanelQueryKey } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'

import { fakeApi, jsonResponse } from '@/test/fake-api'
import { currentUserOrg, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

import { AssistantCreateForm } from './AssistantCreateForm'

const timestamp = '2026-01-01T00:00:00Z'
const org = currentUserOrg({ name: 'Acme' })
const project: VisibleProject = {
  id: fakeId('proj'),
  org_id: org.id,
  name: 'Default',
  created_at: timestamp,
  updated_at: timestamp,
  access: { can_read: true, can_manage: true, can_manage_access: true, can_operate: true },
}
const grant: ProjectModelGrantListItem = {
  grant: {
    id: fakeId('pmog'),
    org_id: org.id,
    project_id: project.id,
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

it('creates with the prefilled project and model, and reports it even though the form is already gone', async () => {
  const restore = enableReactActEnvironment()
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  let panel: AgentPanel = { revision: 0, state: 'empty' }
  const connected: AgentPanel = {
    revision: 1,
    state: 'ready',
    selection: {
      agent_id: fakeId('agt'),
      name: 'Omnara assistant',
      can_operate: true,
      scope: {
        org_id: org.id,
        org_name: org.name,
        project_id: project.id,
        project_name: project.name,
      },
    },
  }
  const createPath = `/api/v1/orgs/${org.id}/assistant-agents`
  let finishRefresh: () => void = () => undefined
  const refresh = new Promise<void>((resolve) => {
    finishRefresh = resolve
  })
  const api = fakeApi([
    {
      method: 'GET',
      path: '/api/v1/me/agent-panel',
      respond: async () => {
        await refresh
        return jsonResponse(panel)
      },
    },
    {
      method: 'GET',
      path: `/api/v1/orgs/${org.id}/projects`,
      respond: () => jsonResponse({ data: [project], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `/api/v1/orgs/${org.id}/projects/${project.id}/model-grants`,
      respond: () => jsonResponse({ data: [grant], next_cursor: null }),
    },
    {
      method: 'POST',
      path: createPath,
      respond: () => {
        panel = connected
        return jsonResponse({ agent_id: fakeId('agt'), panel })
      },
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', maxRetries: 0 })
  client.setConfig({ fetch: api.fetch })
  const cache = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  cache.setQueryData(getAgentPanelQueryKey({ client }), panel)
  const created = vi.fn()
  function Form({ panel: current }: { panel: AgentPanel }) {
    const create = useCreateAssistantAgent()
    return (
      <AssistantCreateForm
        idPrefix="test"
        org={org}
        panel={current}
        create={create}
        onCreated={created}
      />
    )
  }
  function Setup() {
    const { data } = useAgentPanel()
    return data == null || data.selection != null ? null : <Form panel={data} />
  }
  try {
    await act(async () => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>
            <Setup />
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    const create = () =>
      [...container.querySelectorAll('button')].find(
        (item) => item.textContent.trim() === 'Create assistant',
      )
    await waitForUI(() => {
      expect(create()?.disabled).toBe(false)
    })
    await act(async () => {
      create()?.click()
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    // The created panel replaces the card before the follow-up refresh finishes.
    await waitForUI(() => {
      expect(create()).toBeUndefined()
      expect(api.requestsTo('GET', '/api/v1/me/agent-panel')).toHaveLength(1)
    })
    await act(async () => {
      finishRefresh()
      await refresh
    })
    await waitForUI(() => {
      expect(created).toHaveBeenCalledOnce()
    })
    expect(api.requestsTo('POST', createPath).map(({ body }) => body)).toMatchObject([
      { project_id: project.id, model_id: grant.model.id, automatic: false },
    ])
  } finally {
    finishRefresh()
    act(() => {
      root.unmount()
    })
    cache.clear()
    container.remove()
    restore()
  }
})
