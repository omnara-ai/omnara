/** @vitest-environment happy-dom */

import type * as OmnaraReact from '@omnara/react'
import type { ToolCatalog, ToolPermissionProfile } from '@omnara/sdk'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'

import { AgentConfigMcpServersField } from '@/components/agents/AgentConfigMcpServersField'
import { AgentConfigToolsField } from '@/components/agents/AgentConfigToolsField'
import { type BasicMcpServer, emptyBasicConfig } from '@/components/agents/useAgentBuilderForm'

vi.mock('@omnara/react', async (importOriginal) => {
  const actual = await importOriginal<typeof OmnaraReact>()
  return { ...actual, useServerInfo: () => ({ data: undefined }) }
})

vi.mock('@/components/agents/AgentConfigMcpServerTools', () => ({
  AgentConfigMcpServerTools: () => null,
}))

vi.mock('@/components/mcp/McpServerIdentityGroup', () => ({
  McpServerIdentityGroup: () => null,
}))

vi.mock('@/components/secrets/McpOAuthOutcomeDialog', () => ({
  McpOAuthOutcomeDialog: () => null,
}))

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
      configurable: true,
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

let container: HTMLDivElement
let root: Root
let previousActEnvironment: boolean | undefined

beforeAll(() => {
  const actEnvironment = globalThis as typeof globalThis & {
    IS_REACT_ACT_ENVIRONMENT?: boolean
  }
  previousActEnvironment = actEnvironment.IS_REACT_ACT_ENVIRONMENT
  actEnvironment.IS_REACT_ACT_ENVIRONMENT = true
})

afterAll(() => {
  const actEnvironment = globalThis as typeof globalThis & {
    IS_REACT_ACT_ENVIRONMENT?: boolean
  }
  actEnvironment.IS_REACT_ACT_ENVIRONMENT = previousActEnvironment
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

async function renderAndFlush(node: ReactNode) {
  await act(async () => {
    root.render(node)
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

it('preserves an inherited built-in permission when the catalog loads', async () => {
  const onToolsChange = vi.fn()
  const tools = [{ name: 'download_artifact', permission: null }]

  await renderAndFlush(
    <form>
      <AgentConfigToolsField tools={tools} onToolsChange={onToolsChange} />
    </form>,
  )
  await renderAndFlush(
    <form>
      <AgentConfigToolsField catalog={catalog} tools={tools} onToolsChange={onToolsChange} />
    </form>,
  )

  expect(onToolsChange).not.toHaveBeenCalled()
  expect(container.textContent).toContain('Always allow')
})

it('keeps runtime-injected channel tools out of agent config', async () => {
  const channelTools = ['list_channels', 'send_channel_message'].map((name) => ({
    name,
    description: `${name} is derived from active channel bindings.`,
    configurable: false,
    default_permission: alwaysAllowProfile.default_permission,
    permission_modes: alwaysAllowProfile.permission_modes,
  }))
  const onToolsChange = vi.fn()

  await renderAndFlush(
    <form>
      <AgentConfigToolsField
        catalog={{ ...catalog, built_in_tools: channelTools }}
        tools={channelTools.map(({ name }) => ({ name, permission: null }))}
        onToolsChange={onToolsChange}
      />
    </form>,
  )

  expect(container.textContent).not.toContain('list_channels')
  expect(container.textContent).not.toContain('send_channel_message')
  expect(
    container.querySelector<HTMLButtonElement>('button[aria-label="Add tools"]')?.disabled,
  ).toBe(true)
  expect(onToolsChange).not.toHaveBeenCalled()
})

it('treats an old catalog entry without configurable as configurable', async () => {
  const oldCatalog: ToolCatalog = {
    ...catalog,
    built_in_tools: [
      {
        name: 'download_artifact',
        description: 'Download an artifact.',
        default_permission: alwaysAllowProfile.default_permission,
        permission_modes: alwaysAllowProfile.permission_modes,
      },
    ],
  }

  await renderAndFlush(
    <form>
      <AgentConfigToolsField catalog={oldCatalog} tools={[]} onToolsChange={vi.fn()} />
    </form>,
  )

  expect(
    container.querySelector<HTMLButtonElement>('button[aria-label="Add tools"]')?.disabled,
  ).toBe(false)
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
    <form>
      <AgentConfigMcpServersField
        orgId="org-test"
        projectId="project-test"
        servers={servers}
        onServersChange={onServersChange}
        builderDraft={builderDraft}
      />
    </form>,
  )
  await renderAndFlush(
    <form>
      <AgentConfigMcpServersField
        orgId="org-test"
        projectId="project-test"
        permissionProfile={catalog.mcp_tool_permissions}
        servers={servers}
        onServersChange={onServersChange}
        builderDraft={builderDraft}
      />
    </form>,
  )

  expect(onServersChange).not.toHaveBeenCalled()
  expect(container.textContent).toContain('Always ask')
})
