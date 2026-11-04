/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { type Actor, createOmnaraClient } from '@omnara/sdk'
import { getActorOptions } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'

import { AgentChatMessage } from '@/components/agents/AgentChatMessage'
import { ActiveOrgContext } from '@/lib/active-org-context'
import { currentUserOrg } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'

const actorLabelCases: {
  provider: Actor['provider']
  metadata: Actor['metadata']
  name: string
  label: string
}[] = [
  {
    provider: 'integration',
    metadata: { source_label: 'Slack' },
    name: 'Pat',
    label: 'Pat · Slack',
  },
  {
    provider: 'integration',
    metadata: { source_label: 'Discord' },
    name: '',
    label: '123 · Discord',
  },
  {
    provider: 'integration',
    metadata: { source_label: 'GitHub' },
    name: 'Pat',
    label: 'Pat · GitHub',
  },
  {
    provider: 'integration',
    metadata: { source_label: ' Future Platform ' },
    name: 'Pat',
    label: 'Pat · Future Platform',
  },
  { provider: 'integration', metadata: {}, name: 'Pat', label: 'Pat · Integration' },
  {
    provider: 'integration',
    metadata: { source_label: '  ' },
    name: 'Pat',
    label: 'Pat · Integration',
  },
  {
    provider: 'external',
    metadata: { source_label: 'Slack' },
    name: 'Pat',
    label: 'Pat · External',
  },
]

it.each(actorLabelCases)(
  'renders $label from the saved actor without an integration lookup',
  ({ provider, metadata, name, label }) => {
    const restoreActEnvironment = enableReactActEnvironment()
    const container = document.createElement('div')
    const root = createRoot(container)
    const fetch = vi.fn(() => Promise.reject(new Error('unexpected live lookup')))
    const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch })
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    })
    const path = { orgID: 'org_test', projectID: 'proj_test', actorID: 'act_test' }
    const activeOrg = currentUserOrg({ id: path.orgID })
    queryClient.setQueryData(['web-config'], {})
    queryClient.setQueryData(getActorOptions({ client, path }).queryKey, {
      id: path.actorID,
      org_id: path.orgID,
      project_id: path.projectID,
      provider,
      provider_tenant_id: 'saved-platform-namespace',
      provider_user_id: '123',
      display_name: name,
      metadata,
      created_at: '2026-09-27T00:00:00Z',
      updated_at: '2026-09-27T00:00:00Z',
    })
    try {
      act(() => {
        root.render(
          <OmnaraClientProvider client={client}>
            <QueryClientProvider client={queryClient}>
              <ActiveOrgContext value={{ activeOrg, orgs: [activeOrg], setActiveOrgId: vi.fn() }}>
                <AgentChatMessage
                  orgID={path.orgID}
                  projectID={path.projectID}
                  agentID="agt_test"
                  message={{
                    id: 'input_test',
                    role: 'user',
                    metadata: { actorId: path.actorID },
                    parts: [{ id: 'text_test', type: 'text', text: 'Historical input' }],
                  }}
                />
              </ActiveOrgContext>
            </QueryClientProvider>
          </OmnaraClientProvider>,
        )
      })
      expect(container.textContent).toContain(label)
      expect(fetch).not.toHaveBeenCalled()
    } finally {
      act(() => {
        root.unmount()
      })
      queryClient.clear()
      restoreActEnvironment()
    }
  },
)

it('nests the tool calls a running command made under its run_command call', async () => {
  const restoreActEnvironment = enableReactActEnvironment()
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  const path = {
    orgID: `org_${'a'.repeat(26)}`,
    projectID: `proj_${'b'.repeat(26)}`,
    agentID: `agt_${'c'.repeat(26)}`,
  }
  const parentID = `tcl_${'d'.repeat(26)}`
  const requestedURLs: string[] = []
  const fetch = vi.fn((input: RequestInfo | URL) => {
    requestedURLs.push(input instanceof Request ? input.url : String(input))
    return Promise.resolve(
      new Response(
        JSON.stringify({
          data: [
            {
              id: `tcl_${'e'.repeat(26)}`,
              agent_id: path.agentID,
              turn_id: `trn_${'f'.repeat(26)}`,
              provider_call_id: 'program',
              caller: 'program',
              parent_tool_call_id: parentID,
              name: 'web_search',
              input: { query: 'omnara' },
              type: 'built_in',
              state: 'awaiting_permission',
              created_at: '2026-11-04T00:00:00Z',
            },
          ],
          next_cursor: null,
        }),
        { headers: { 'Content-Type': 'application/json' } },
      ),
    )
  })
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch })
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  const activeOrg = currentUserOrg({ id: path.orgID })
  queryClient.setQueryData(['web-config'], {})
  try {
    act(() => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={queryClient}>
            <ActiveOrgContext value={{ activeOrg, orgs: [activeOrg], setActiveOrgId: vi.fn() }}>
              <AgentChatMessage
                {...path}
                message={{
                  id: 'turn:trn_test',
                  role: 'assistant',
                  parts: [
                    {
                      id: 'tool_test',
                      type: 'dynamic-tool',
                      toolCallId: parentID,
                      toolName: 'run_command',
                      state: 'input-available',
                      input: { command: 'omnarad call web_search' },
                    },
                  ],
                }}
              />
            </ActiveOrgContext>
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
    })
    await vi.waitFor(() => {
      expect(container.textContent).toContain('1 tool call')
    })
    expect(requestedURLs).toHaveLength(1)
    expect(new URL(requestedURLs[0] ?? '').searchParams.get('parent_tool_call_id')).toBe(parentID)

    await act(async () => {
      container.querySelector('button')?.click()
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    const calls = container.querySelector('[aria-label="Tool calls from this command"]')
    expect(calls?.textContent).toContain('web_search')
    expect(calls?.textContent).toContain('Awaiting approval')
  } finally {
    act(() => {
      root.unmount()
    })
    container.remove()
    queryClient.clear()
    restoreActEnvironment()
  }
})
