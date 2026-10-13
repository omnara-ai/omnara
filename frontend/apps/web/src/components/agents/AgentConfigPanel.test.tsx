/** @vitest-environment happy-dom */

import { type Agent, type AgentConfig } from '@omnara/sdk'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'

import { AgentCompiledConfig } from '@/components/agents/AgentCompiledConfig'
import { AgentConfigPanel } from '@/components/agents/AgentConfigPanel'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { agentConfigModel, fakeId } from '@/test/fixtures'
import { renderIntegration } from '@/test/integration-render'
import { enableReactActEnvironment } from '@/test/react-act'

it('keeps the saved compiled view separate from an unsaved form', async () => {
  const restore = enableReactActEnvironment()
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  const onSubmit = vi.fn((event: React.SyntheticEvent) => {
    event.preventDefault()
  })
  const definition: AgentConfig['compiled_definition'] = {
    instruction: 'Saved instruction.',
    model: { configured_model_id: fakeId('mdl') },
    tools: { read_file: { enabled: true, permission: { mode: 'always_allow', parameters: {} } } },
  }
  try {
    act(() => {
      root.render(
        <form onSubmit={onSubmit}>
          <textarea aria-label="Draft" defaultValue="Unsaved instruction." />
          <AgentCompiledConfig definition={definition} />
        </form>,
      )
    })
    expect(container.querySelector('pre')).toBeNull()
    const draft = container.querySelector('textarea')
    const trigger = container.querySelector<HTMLButtonElement>('[data-slot="collapsible-trigger"]')
    expect(trigger?.textContent).toBe('Compiled config (saved)')
    expect(trigger?.getAttribute('aria-expanded')).toBe('false')
    act(() => {
      trigger?.click()
    })
    await vi.waitFor(() => {
      expect(JSON.parse(container.querySelector('pre')?.textContent ?? '')).toEqual(definition)
    })
    expect(container.querySelector('textarea')).toBe(draft)
    expect(draft?.value).toBe('Unsaved instruction.')
    expect(onSubmit).not.toHaveBeenCalled()
    act(() => {
      trigger?.click()
    })
    expect(trigger?.getAttribute('aria-expanded')).toBe('false')
    expect(draft?.value).toBe('Unsaved instruction.')
    expect(onSubmit).not.toHaveBeenCalled()
  } finally {
    act(() => {
      root.unmount()
    })
    container.remove()
    restore()
  }
})

it.each([false, true])(
  'opens the editor from the generated source of a launched agent (subagent: %s)',
  async (subagent) => {
    const restore = enableReactActEnvironment()
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    const orgId = fakeId('org')
    const projectId = fakeId('proj')
    const configId = fakeId('acfg')
    const onDirtyChange = vi.fn()
    const agent: Agent = {
      id: fakeId('agt'),
      org_id: orgId,
      project_id: projectId,
      name: 'Launched',
      state: 'active',
      parent_agent_id: subagent ? fakeId('agt') : undefined,
      current_config_id: configId,
      created_at: '2026-09-16T00:00:00Z',
      updated_at: '2026-09-16T00:00:00Z',
    }
    const config: AgentConfig = {
      id: configId,
      org_id: orgId,
      project_id: projectId,
      created_at: '2026-09-16T00:00:00Z',
      effective_definition_hash: 'hash',
      model: agentConfigModel(),
      compiled_definition: {
        instruction: 'Help with tickets.',
        model: { configured_model_id: fakeId('mdl') },
      },
    }
    const configPath = `/api/v1/orgs/${orgId}/projects/${projectId}/agent-configs/${configId}`
    const api = fakeApi([
      { method: 'GET', path: configPath, respond: () => Response.json(config) },
      {
        method: 'GET',
        path: `${configPath}/generated-source`,
        respond: () =>
          api.requestsTo('GET', `${configPath}/generated-source`).length === 1
            ? jsonResponse({ code: 'unavailable', message: 'Try again.' }, 503)
            : Response.json({
                source:
                  'instruction: Help with tickets.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n',
              }),
      },
    ])
    try {
      const { cache } = renderIntegration(
        root,
        api,
        <AgentConfigPanel
          orgId={orgId}
          projectId={projectId}
          agent={agent}
          canManage={false}
          onClose={vi.fn()}
          onDirtyChange={onDirtyChange}
        />,
      )
      await vi.waitFor(() => {
        expect(container.textContent).toContain('Couldn’t load the agent config.')
      })
      act(() => {
        ;[...container.querySelectorAll('button')]
          .find((button) => button.textContent === 'Retry')
          ?.click()
      })
      await vi.waitFor(() => {
        expect(container.querySelector('form')).not.toBeNull()
      })
      expect(api.requestsTo('GET', `${configPath}/generated-source`)).toHaveLength(2)
      expect(onDirtyChange).not.toHaveBeenCalledWith(true)
      cache.clear()
    } finally {
      act(() => {
        root.unmount()
      })
      container.remove()
      restore()
    }
  },
)
