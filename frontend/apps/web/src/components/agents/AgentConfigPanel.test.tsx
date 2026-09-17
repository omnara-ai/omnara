/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { type Agent, type AgentConfig, createOmnaraClient } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'

import { AgentConfigPanel } from '@/components/agents/AgentConfigPanel'
import { fakeApi } from '@/test/fake-api'
import { agentConfigModel, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'

it.each([undefined, 'instruction: Old generated source.'])(
  'shows a read-only notice instead of the subagent editor (source: %s)',
  async (source) => {
    const restore = enableReactActEnvironment()
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    const onClose = vi.fn()
    const onDirtyChange = vi.fn()
    const orgId = fakeId('org')
    const projectId = fakeId('proj')
    const configId = fakeId('acfg')
    const configPath = `/api/v1/orgs/${orgId}/projects/${projectId}/agent-configs/${configId}`
    const agent: Agent = {
      id: fakeId('agt'),
      org_id: orgId,
      project_id: projectId,
      name: 'Child',
      state: 'active',
      parent_agent_id: fakeId('agt'),
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
      source,
    }
    const api = fakeApi([
      {
        method: 'GET',
        path: configPath,
        respond: () => Response.json(config),
      },
    ])
    const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
    client.setConfig({ fetch: api.fetch })
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    try {
      await act(async () => {
        root.render(
          <OmnaraClientProvider client={client}>
            <QueryClientProvider client={queryClient}>
              <AgentConfigPanel
                orgId={orgId}
                projectId={projectId}
                agent={agent}
                canManage
                onClose={onClose}
                onDirtyChange={onDirtyChange}
              />
            </QueryClientProvider>
          </OmnaraClientProvider>,
        )
        await new Promise((resolve) => setTimeout(resolve, 0))
      })
      await vi.waitFor(() => {
        expect(container.textContent).toContain('This derived configuration is read-only.')
      })
      expect(container.textContent).not.toContain('Old generated source.')
      expect(container.textContent).not.toContain('Save config')
      expect(container.querySelector('form')).toBeNull()
      expect(api.requests.map(({ method, url }) => [method, url.pathname])).toEqual([
        ['GET', configPath],
      ])
      expect(onDirtyChange).not.toHaveBeenCalled()
      act(() => {
        ;[...container.querySelectorAll('button')]
          .find((button) => button.textContent === 'Close')
          ?.click()
      })
      expect(onClose).toHaveBeenCalledOnce()
    } finally {
      act(() => {
        root.unmount()
      })
      queryClient.clear()
      container.remove()
      restore()
    }
  },
)
