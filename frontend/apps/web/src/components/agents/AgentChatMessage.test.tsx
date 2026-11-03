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
