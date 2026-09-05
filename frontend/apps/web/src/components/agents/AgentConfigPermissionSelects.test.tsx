/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type ToolCatalog, type ToolPermissionProfile } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'

import { AgentConfigMcpServersField } from '@/components/agents/AgentConfigMcpServersField'
import { AgentConfigToolsField } from '@/components/agents/AgentConfigToolsField'
import { type BasicMcpServer, emptyBasicConfig } from '@/components/agents/useAgentBuilderForm'
import { ActiveOrgContext } from '@/lib/active-org-context'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { currentUserOrg } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'

const alwaysAllowProfile: ToolPermissionProfile = {
  default_permission: { mode: 'always_allow', parameters: {} },
  permission_modes: [
    {
      name: 'always_allow',
      label: 'Always allow',
      description: 'Always allow.',
      parameters_schema: {},
    },
    {
      name: 'always_ask',
      label: 'Always ask',
      description: 'Always ask.',
      parameters_schema: {},
    },
  ],
}

const catalog: ToolCatalog = {
  built_in_tools: [
    {
      name: 'download_artifact',
      description: 'Download an artifact.',
      default_permission: alwaysAllowProfile.default_permission,
      permission_modes: alwaysAllowProfile.permission_modes,
    },
  ],
  custom_tool_permissions: alwaysAllowProfile,
  mcp_tool_permissions: {
    ...alwaysAllowProfile,
    default_permission: { mode: 'always_ask', parameters: {} },
  },
}

const activeOrg = currentUserOrg({ id: 'org-test', name: 'Test org' })

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
  Providers = testProviders()
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

function testProviders() {
  const api = fakeApi([
    {
      method: 'GET',
      path: '/api/v1/mcp-servers',
      respond: () => jsonResponse({ data: [], next_cursor: null }),
    },
    {
      method: 'POST',
      path: '/api/v1/orgs/org-test/projects/project-test/mcp-servers/tools',
      respond: () =>
        jsonResponse({
          protocol_version: '2025-06-18',
          server_info: { name: 'example', version: '1.0.0' },
          tools: [],
        }),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return function Providers({ children }: { children: ReactNode }) {
    return (
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <ActiveOrgContext.Provider
            value={{ orgs: [activeOrg], activeOrg, setActiveOrgId: () => undefined }}
          >
            <form>{children}</form>
          </ActiveOrgContext.Provider>
        </QueryClientProvider>
      </OmnaraClientProvider>
    )
  }
}

let Providers: ReturnType<typeof testProviders>

async function renderAndFlush(node: ReactNode) {
  await act(async () => {
    root.render(<Providers>{node}</Providers>)
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

it('preserves an inherited built-in permission when the catalog loads', async () => {
  const onToolsChange = vi.fn()
  const tools = [{ name: 'download_artifact', permission: null }]

  await renderAndFlush(<AgentConfigToolsField tools={tools} onToolsChange={onToolsChange} />)
  await renderAndFlush(
    <AgentConfigToolsField catalog={catalog} tools={tools} onToolsChange={onToolsChange} />,
  )

  expect(onToolsChange).not.toHaveBeenCalled()
  expect(container.textContent).toContain('Always allow')
})

it('preserves an inherited MCP permission when its profile loads', async () => {
  const onServersChange = vi.fn()
  const server: BasicMcpServer = {
    id: 'server-1',
    name: 'example',
    url: 'https://example.com/mcp',
    permission: null,
    defaultEnabled: true,
    authType: 'none',
    secretId: '',
    service: '',
    region: '',
    tools: [],
  }
  const servers = [server]
  const builderDraft = { ...emptyBasicConfig, mcpServers: servers }

  await renderAndFlush(
    <AgentConfigMcpServersField
      orgId="org-test"
      projectId="project-test"
      servers={servers}
      onServersChange={onServersChange}
      builderDraft={builderDraft}
    />,
  )
  await renderAndFlush(
    <AgentConfigMcpServersField
      orgId="org-test"
      projectId="project-test"
      permissionProfile={catalog.mcp_tool_permissions}
      servers={servers}
      onServersChange={onServersChange}
      builderDraft={builderDraft}
    />,
  )

  expect(onServersChange).not.toHaveBeenCalled()
  expect(container.textContent).toContain('Always ask')
})
