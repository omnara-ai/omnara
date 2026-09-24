/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { type Agent, type AgentConfig, createOmnaraClient } from '@omnara/sdk'
import { getAgentConfigQueryKey } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it } from 'vitest'

import { useAgentEffort } from '@/components/agents/useAgentEffort'
import { fakeApi, type FakeRoute, jsonResponse } from '@/test/fake-api'
import { agentConfigModel, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'

const orgId = fakeId('org')
const projectId = fakeId('proj')
const agentId = fakeId('agt')
const configId = fakeId('acfg')
const nextConfigId = `acfg_${'b'.repeat(26)}`
const updatePath = `/api/v1/orgs/${orgId}/projects/${projectId}/agents/${agentId}/config`
const source = `instruction: Help.
model:
  provider_config: openai
  name: gpt
`

const agent: Agent = {
  id: agentId,
  org_id: orgId,
  project_id: projectId,
  name: 'Agent',
  state: 'active',
  current_config_id: configId,
  created_at: '2026-09-16T00:00:00Z',
  updated_at: '2026-09-16T00:00:00Z',
}

function agentConfig(overrides: Partial<AgentConfig> = {}): AgentConfig {
  return {
    id: configId,
    org_id: orgId,
    project_id: projectId,
    created_at: '2026-09-16T00:00:00Z',
    effective_definition_hash: 'hash',
    model: agentConfigModel({
      supports_reasoning: true,
      default_reasoning_effort: 'medium',
      supported_reasoning_efforts: ['high', 'medium', 'low'],
    }),
    source,
    source_format: 'yaml',
    compiled_definition: {
      instruction: 'Help.',
      model: { configured_model_id: fakeId('mdl') },
      tools: {},
    },
    ...overrides,
  }
}

function renderEffort({
  config,
  agentOverrides = {},
  canManage = true,
  routes = [],
}: {
  config: AgentConfig
  agentOverrides?: Partial<Agent>
  canManage?: boolean
  routes?: FakeRoute[]
}) {
  const restore = enableReactActEnvironment()
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  const api = fakeApi(routes)
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  function Probe({ config }: { config: AgentConfig }) {
    const effort = useAgentEffort({
      orgId,
      projectId,
      agent: { ...agent, ...agentOverrides },
      config,
      canManage,
    })
    if (effort === null) return null
    return (
      <output
        data-value={effort.value}
        data-editable={String(effort.editable)}
        data-error={effort.error ?? ''}
      >
        {effort.options.map((option) => (
          <button
            key={option}
            type="button"
            onClick={() => {
              effort.change(option)
            }}
          >
            {option}
          </button>
        ))}
      </output>
    )
  }

  const render = (config: AgentConfig) => {
    act(() => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={queryClient}>
            <Probe config={config} />
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
    })
  }
  render(config)
  const state = () => {
    const output = container.querySelector('output')
    return output === null
      ? null
      : {
          value: output.dataset.value,
          editable: output.dataset.editable === 'true',
          error: output.dataset.error,
        }
  }
  const choose = (effort: string) => {
    ;[...container.querySelectorAll('button')]
      .find((button) => button.textContent === effort)
      ?.click()
  }
  const mutationSettled = async () => {
    await new Promise<void>((resolve) => {
      const unsubscribe = queryClient.getMutationCache().subscribe((event) => {
        const status = event.mutation?.state.status
        if (status === 'success' || status === 'error') {
          unsubscribe()
          resolve()
        }
      })
    })
    await new Promise((resolve) => setTimeout(resolve, 0))
  }
  return {
    api,
    client,
    queryClient,
    state,
    choose,
    mutationSettled,
    render,
    cleanup: () => {
      act(() => {
        root.unmount()
      })
      queryClient.clear()
      container.remove()
      restore()
    },
  }
}

it('hides the effort control when the model has no supported efforts', () => {
  const view = renderEffort({ config: agentConfig({ model: agentConfigModel() }) })
  try {
    expect(view.state()).toBeNull()
  } finally {
    view.cleanup()
  }
})

it.each([
  ['a subagent', { parent_agent_id: fakeId('agt') }, true],
  ['a viewer without manage access', {}, false],
  ['an archived agent', { state: 'archived' as const }, true],
])('shows the effort read-only for %s', (_, agentOverrides, canManage) => {
  const view = renderEffort({ config: agentConfig(), agentOverrides, canManage })
  try {
    expect(view.state()).toMatchObject({ value: 'medium', editable: false })
  } finally {
    view.cleanup()
  }
})

it('updates the agent config with the chosen effort', async () => {
  const nextConfig = agentConfig({
    id: nextConfigId,
    model: agentConfigModel({
      supports_reasoning: true,
      default_reasoning_effort: 'low',
      supported_reasoning_efforts: ['high', 'medium', 'low'],
    }),
  })
  let respond: (response: Response) => void = () => undefined
  const pending = new Promise<Response>((resolve) => {
    respond = resolve
  })
  const view = renderEffort({
    config: agentConfig(),
    routes: [{ method: 'POST', path: updatePath, respond: () => pending }],
  })
  try {
    await act(async () => {
      view.choose('low')
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    expect(view.state()).toMatchObject({ value: 'low', editable: false })
    expect(view.api.requestsTo('POST', updatePath).map(({ body }) => body)).toEqual([
      {
        source: `${source}  reasoning:\n    effort: low\n`,
        source_format: 'yaml',
        expected_current_config_id: configId,
      },
    ])

    await act(async () => {
      const settled = view.mutationSettled()
      respond(
        Response.json({
          agent_config: nextConfig,
          agent_input: {
            id: fakeId('ain'),
            agent_id: agentId,
            state: 'received',
            delivery_mode: 'queued',
            input_kind: 'config_change',
            queued_at: '2026-09-16T00:00:00Z',
          },
          event_id: fakeId('evt'),
        }),
      )
      await settled
    })
    expect(
      view.queryClient.getQueryData(
        getAgentConfigQueryKey({
          path: { orgID: orgId, projectID: projectId, agentConfigID: nextConfigId },
          client: view.client,
        }),
      ),
    ).toEqual(nextConfig)
    expect(view.state()).toMatchObject({ editable: true, error: '' })
  } finally {
    view.cleanup()
  }
})

it('explains a conflicting config update', async () => {
  const view = renderEffort({
    config: agentConfig(),
    routes: [
      {
        method: 'POST',
        path: updatePath,
        respond: () => jsonResponse({ code: 'conflict', message: 'Config changed.' }, 409),
      },
    ],
  })
  try {
    await act(async () => {
      const settled = view.mutationSettled()
      view.choose('high')
      await settled
    })
    expect(view.state()).toMatchObject({
      value: 'medium',
      editable: true,
      error: 'The agent’s configuration changed. Choose the effort again.',
    })
  } finally {
    view.cleanup()
  }
})

it('drops a failed effort update once the config changes', async () => {
  const view = renderEffort({
    config: agentConfig(),
    routes: [
      {
        method: 'POST',
        path: updatePath,
        respond: () => jsonResponse({ code: 'conflict', message: 'Config changed.' }, 409),
      },
    ],
  })
  try {
    await act(async () => {
      const settled = view.mutationSettled()
      view.choose('high')
      await settled
    })
    view.render(agentConfig({ id: nextConfigId }))
    expect(view.state()).toMatchObject({ value: 'medium', editable: true, error: '' })
  } finally {
    view.cleanup()
  }
})

it('submits a json config as json', async () => {
  const view = renderEffort({
    config: agentConfig({
      source: '{"instruction": "Help.", "model": {"provider_config": "openai", "name": "gpt"}}',
      source_format: 'json',
    }),
    routes: [
      {
        method: 'POST',
        path: updatePath,
        respond: () => jsonResponse({ code: 'conflict', message: 'Config changed.' }, 409),
      },
    ],
  })
  try {
    await act(async () => {
      const settled = view.mutationSettled()
      view.choose('low')
      await settled
    })
    expect(view.api.requestsTo('POST', updatePath).map(({ body }) => body)).toEqual([
      {
        source: JSON.stringify(
          {
            instruction: 'Help.',
            model: { provider_config: 'openai', name: 'gpt', reasoning: { effort: 'low' } },
          },
          null,
          2,
        ),
        source_format: 'json',
        expected_current_config_id: configId,
      },
    ])
  } finally {
    view.cleanup()
  }
})
