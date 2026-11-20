/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type Secret } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { AgentConfigMcpSecretCombobox } from '@/components/agents/AgentConfigMcpSecretCombobox'
import type { BasicMcpServer } from '@/components/agents/useAgentBuilderForm'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

const orgId = fakeId('org')
const projectId = fakeId('proj')
const secretId = fakeId('sec')
const projectPath = `/api/v1/orgs/${orgId}/projects/${projectId}`

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

it.each<{
  case: string
  kind: Secret['kind']
  metadata: Secret['metadata']
  label: string
  options: string[]
}>([
  {
    case: 'no metadata URL',
    kind: 'oauth_token_set',
    metadata: {},
    label: 'grafana-oauth',
    options: ['grafana-oauth'],
  },
  {
    case: 'a different metadata URL',
    kind: 'oauth_token_set',
    metadata: { mcp_url: 'https://mcp.example/other' },
    label: 'grafana-oauth',
    options: ['grafana-oauth'],
  },
  { case: 'the wrong kind', kind: 'generic', metadata: {}, label: 'Select secret', options: [] },
])('shows an assigned secret with $case as $label', async ({ kind, metadata, label, options }) => {
  const secret: Secret = {
    id: secretId,
    org_id: orgId,
    owner: { kind: 'project', project_id: projectId },
    name: 'grafana-oauth',
    kind,
    management_kind: 'tenant',
    metadata,
    current_version_number: 1,
    payload_keys: ['access_token', 'mcp_url'],
    created_at: '2026-11-19T00:00:00Z',
    updated_at: '2026-11-19T00:00:00Z',
  }
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/secrets`,
      respond: () => jsonResponse({ data: [], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `${projectPath}/secrets/${secretId}`,
      respond: () =>
        jsonResponse({ secret, availability: { source: 'direct', project_id: projectId } }),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', maxRetries: 0 })
  client.setConfig({ fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const server: BasicMcpServer = {
    id: 'server-1',
    name: 'grafana',
    url: 'https://mcp.example/grafana',
    permission: null,
    defaultEnabled: true,
    authType: 'oauth',
    secretId,
    service: '',
    region: '',
    tools: [],
  }

  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <AgentConfigMcpSecretCombobox
            orgId={orgId}
            projectId={projectId}
            server={server}
            onChange={() => undefined}
          />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })
  await waitForUI(() => {
    expect(queryClient.isFetching()).toBe(0)
  })
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })

  expect(api.requestsTo('GET', `${projectPath}/secrets/${secretId}`)).toHaveLength(1)
  const trigger = container.querySelector('[role="combobox"]')
  expect(trigger?.textContent).toContain(label)

  act(() => {
    trigger?.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="listbox"]')).not.toBeNull()
  })
  expect(
    [...document.querySelectorAll('[role="option"]')].map((option) => option.textContent),
  ).toEqual(options)
})
