/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import {
  createOmnaraClient,
  type MachinePoolSummary,
  type ToolCatalog,
  type ToolPermissionProfile,
} from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterContextProvider,
} from '@tanstack/react-router'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import { parse } from 'yaml'

import { AgentConfigMcpServersField } from '@/components/agents/AgentConfigMcpServersField'
import { AgentConfigToolsField } from '@/components/agents/AgentConfigToolsField'
import {
  type BasicMcpServer,
  createBasicConfigSession,
  emptyBasicConfig,
  useAgentBuilderForm,
} from '@/components/agents/useAgentBuilderForm'
import { useAgentDraft } from '@/components/agents/useAgentDraft'
import { useProjectDefaults } from '@/components/agents/useProjectDefaults'
import { ActiveOrgContext } from '@/lib/active-org-context'
import { fakeApi, type FakeRoute, jsonResponse } from '@/test/fake-api'
import { currentUserOrg, machinePool, projectMachinePoolGrant } from '@/test/fixtures'
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
      name: 'web_search',
      description: 'Search the web.',
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

const includedCatalog: ToolCatalog = {
  ...catalog,
  built_in_tools: ['run_command', 'skill', 'send_integration_message'].map((name) => ({
    name,
    description: name,
    implicit: true,
    default_permission: alwaysAllowProfile.default_permission,
    permission_modes:
      name === 'send_integration_message'
        ? alwaysAllowProfile.permission_modes.slice(0, 1)
        : alwaysAllowProfile.permission_modes,
  })),
}

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

function testProviders(routes: FakeRoute[] = []) {
  const api = fakeApi([
    ...routes,
    {
      method: 'POST',
      path: '/api/v1/orgs/org-test/projects/project-test/agent-configs/tools',
      respond: () => jsonResponse({ tools: [] }),
    },
    {
      method: 'GET',
      path: '/api/v1/tool-catalog',
      respond: () => Response.json(includedCatalog),
    },
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
  const router = createRouter({
    routeTree: createRootRoute(),
    history: createMemoryHistory(),
  })
  return function Providers({ children }: { children: ReactNode }) {
    return (
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <ActiveOrgContext.Provider
            value={{ orgs: [activeOrg], activeOrg, setActiveOrgId: () => undefined }}
          >
            <RouterContextProvider router={router}>
              <form>{children}</form>
            </RouterContextProvider>
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
  const tools = [{ name: 'web_search', permission: null }]

  await renderAndFlush(<AgentConfigToolsField tools={tools} onToolsChange={onToolsChange} />)
  await renderAndFlush(
    <AgentConfigToolsField catalog={catalog} tools={tools} onToolsChange={onToolsChange} />,
  )

  expect(onToolsChange).not.toHaveBeenCalled()
  expect(container.textContent).toContain('Always allow')
  expect(container.textContent).toContain('web_search')
})

it('waits for the catalog before showing default tools and preserves their overrides', async () => {
  const onToolsChange = vi.fn()
  const tools = [
    { name: 'run_command', enabled: false, permission: null },
    { name: 'skill', permission: { mode: 'always_ask', parameters: {} } },
  ]
  await renderAndFlush(<AgentConfigToolsField tools={tools} onToolsChange={onToolsChange} />)
  expect(container.querySelector('[aria-label^="Remove "]')).toBeNull()
  expect(container.querySelector('[data-slot="collapsible-trigger"]')).toBeNull()
  await renderAndFlush(
    <AgentConfigToolsField catalog={includedCatalog} tools={tools} onToolsChange={onToolsChange} />,
  )
  expect(container.querySelector('[aria-label^="Remove "]')).toBeNull()
  click('[data-slot="collapsible-trigger"]')
  expect(container.querySelector('[aria-label="run_command permission"]')?.textContent).toBe(
    'Disabled',
  )
  expect(container.querySelector('[aria-label="skill permission"]')?.textContent).toBe('Always ask')
  expect(onToolsChange).not.toHaveBeenCalled()
})

it('uses catalog classification rather than recognizing tool names', async () => {
  const onToolsChange = vi.fn()
  const classifiedCatalog: ToolCatalog = {
    ...catalog,
    built_in_tools: [
      {
        name: 'future_resource_tool',
        description: 'Future tool.',
        implicit: true,
        ...alwaysAllowProfile,
      },
      {
        name: 'run_command',
        description: 'Manual tool.',
        implicit: false,
        ...alwaysAllowProfile,
      },
    ],
  }
  await renderAndFlush(
    <AgentConfigToolsField
      catalog={classifiedCatalog}
      tools={classifiedCatalog.built_in_tools.map(({ name }) => ({ name, permission: null }))}
      onToolsChange={onToolsChange}
    />,
  )
  expect(container.querySelector('[aria-label="Remove run_command"]')).not.toBeNull()
  expect(container.querySelector('[aria-label="Remove future_resource_tool"]')).toBeNull()
  click('[data-slot="collapsible-trigger"]')
  expect(container.querySelector('[aria-label="future_resource_tool permission"]')).not.toBeNull()
  await renderAndFlush(
    <AgentConfigToolsField catalog={classifiedCatalog} tools={[]} onToolsChange={onToolsChange} />,
  )
  act(() => {
    container
      .querySelector('[aria-label="Add tools"]')
      ?.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
  })
  const options = [...document.querySelectorAll('[role="menuitem"]')].map(
    (item) => item.textContent,
  )
  expect(options).toContain('run_command')
  expect(options).not.toContain('future_resource_tool')
})

it('shows and can re-enable an explicitly disabled normal tool', async () => {
  const onToolsChange = vi.fn()
  const tools = [{ name: 'web_search', enabled: false, permission: null }]
  await renderAndFlush(
    <AgentConfigToolsField catalog={catalog} tools={tools} onToolsChange={onToolsChange} />,
  )
  expect(container.querySelector('[aria-label="web_search permission"]')?.textContent).toBe(
    'Disabled',
  )
  await selectIncludedPermission('web_search', 'Always ask')
  expect(onToolsChange).toHaveBeenCalledWith([
    { name: 'web_search', enabled: undefined, permission: { mode: 'always_ask', parameters: {} } },
  ])
})

it('keeps included tools out of normal rows and preserves their overrides', async () => {
  const onToolsChange = vi.fn()
  const machineTools = [
    'run_command',
    'write_process',
    'read_process',
    'stop_process',
    'list_processes',
    'create_machine',
    'delete_machine',
    'list_machines',
    'inspect_machine',
    'upload_file',
    'download_file',
  ].map((name) => ({ name, permission: { mode: 'always_ask', parameters: {} } }))
  const machineCatalog = {
    ...catalog,
    built_in_tools: [
      ...catalog.built_in_tools,
      ...machineTools.map(({ name }) => ({
        name,
        description: name,
        implicit: true,
        ...alwaysAllowProfile,
      })),
    ],
  }
  await renderAndFlush(
    <AgentConfigToolsField
      catalog={machineCatalog}
      tools={[...machineTools, { name: 'web_search', permission: null }]}
      onToolsChange={onToolsChange}
    />,
  )
  for (const { name } of machineTools) {
    expect(container.textContent).not.toContain(name)
  }
  expect(container.querySelector('[aria-label="Add tools"]')?.hasAttribute('disabled')).toBe(true)
  const remove = container.querySelector<HTMLButtonElement>('[aria-label="Remove web_search"]')
  expect(remove).not.toBeNull()
  act(() => {
    remove?.click()
  })
  expect(onToolsChange).toHaveBeenCalledWith(machineTools)
})

it('hides the dropdown when its configured tools are removed', async () => {
  const onToolsChange = vi.fn()
  const tools = [{ name: 'run_command', permission: { mode: 'always_ask', parameters: {} } }]
  await renderAndFlush(
    <AgentConfigToolsField catalog={includedCatalog} tools={tools} onToolsChange={onToolsChange} />,
  )
  expect(container.textContent).not.toContain('run_command')
  await renderAndFlush(
    <AgentConfigToolsField catalog={includedCatalog} tools={[]} onToolsChange={onToolsChange} />,
  )
  expect(container.textContent).not.toContain('run_command')
  expect(container.querySelector('[data-slot="collapsible-trigger"]')).toBeNull()
  expect(onToolsChange).not.toHaveBeenCalled()
  expect(container.querySelector('[aria-label="Remove run_command"]')).toBeNull()
})

function click(selector: string) {
  const button = container.querySelector<HTMLButtonElement>(selector)
  if (!button) throw new Error(`Missing ${selector}`)
  act(() => {
    button.click()
  })
}

it.each(['run_command', 'skill', 'send_integration_message'])(
  'displays the catalog default for configured %s without changing its source',
  async (name) => {
    const onToolsChange = vi.fn()
    await renderAndFlush(
      <AgentConfigToolsField tools={[{ name, permission: null }]} onToolsChange={onToolsChange} />,
    )
    expect(container.querySelector('[data-slot="collapsible-trigger"]')).toBeNull()
    expect(container.querySelector('[role="radiogroup"]')).toBeNull()
    await renderAndFlush(
      <AgentConfigToolsField
        catalog={includedCatalog}
        tools={[{ name, permission: null }]}
        onToolsChange={onToolsChange}
      />,
    )
    click('[data-slot="collapsible-trigger"]')
    expect(container.textContent).toContain(name)
    expect(container.textContent).toContain('Always allow')
    expect(container.querySelector('[role="radio"]')?.hasAttribute('disabled')).toBe(false)
    expect(onToolsChange).not.toHaveBeenCalled()
  },
)

const includedSource = `instruction: Test included tools.
model:
  provider_config: openai
  name: primary
tools:
  web_search:
    permission:
      mode: always_ask
`

const defaultIncludedSource = `${includedSource}  run_command: {}\n`

function IncludedToolsHarness({ source = defaultIncludedSource }: { source?: string }) {
  const form = useAgentBuilderForm(createBasicConfigSession(source), undefined, {
    orgId: 'org-test',
    projectId: 'project-test',
  })
  return (
    <>
      <AgentConfigToolsField
        catalog={includedCatalog}
        tools={form.tools}
        onToolsChange={form.setTools}
      />
      <output>{form.yaml}</output>
    </>
  )
}

async function selectIncludedPermission(name: string, label: string) {
  const option = container.querySelector<HTMLButtonElement>(
    `[aria-label="${name} permission"] [role="radio"][aria-label="${label}"]`,
  )
  if (!option) throw new Error(`Missing ${label} option`)
  await act(async () => {
    option.click()
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

it.each(['run_command', 'skill', 'send_integration_message'])(
  'disables and re-enables %s without changing other tools',
  async (name) => {
    await renderAndFlush(<IncludedToolsHarness source={`${includedSource}  ${name}: {}\n`} />)
    click('[data-slot="collapsible-trigger"]')
    await selectIncludedPermission(name, 'Disabled')
    const saved = container.querySelector('output')?.textContent ?? ''
    expect(parse(saved)).toHaveProperty('tools', {
      web_search: { permission: { mode: 'always_ask' } },
      [name]: { type: 'built_in', enabled: false },
    })
    expect(createBasicConfigSession(saved).initialDraft).not.toBeNull()
    expect(container.querySelector(`[aria-label="${name} permission"]`)?.textContent).toBe(
      'Disabled',
    )
    await selectIncludedPermission(name, 'Always allow')
    const enabled = container.querySelector('output')?.textContent ?? ''
    expect(parse(enabled)).toHaveProperty(`tools.${name}`, {
      type: 'built_in',
      permission: { mode: 'always_allow' },
    })
    expect(container.querySelector(`[aria-label="${name} permission"]`)?.textContent).toBe(
      'Always allow',
    )
  },
)

it.each([false, true])(
  'keeps the tool explicit when selecting its default permission (disabled: %s)',
  async (disabled) => {
    const source = `${includedSource}  run_command:
    enabled: ${!disabled}
    permission:
      mode: always_ask
`
    await renderAndFlush(<IncludedToolsHarness source={source} />)
    click('[data-slot="collapsible-trigger"]')
    expect(container.querySelector('output')?.textContent).toBe(source)
    await selectIncludedPermission('run_command', 'Always allow')
    const saved = container.querySelector('output')?.textContent ?? ''
    expect(parse(saved)).toHaveProperty('tools', {
      web_search: { permission: { mode: 'always_ask' } },
      run_command: { type: 'built_in', permission: { mode: 'always_allow' } },
    })
  },
)

it('reopens disabled tools and preserves their permission until a new one is chosen', async () => {
  const source = `${includedSource}  run_command:
    enabled: false
    permission:
      mode: always_ask
`
  await renderAndFlush(<IncludedToolsHarness source={source} />)
  click('[data-slot="collapsible-trigger"]')
  expect(container.querySelector('output')?.textContent).toBe(source)
  expect(container.querySelector('[aria-label="run_command permission"]')?.textContent).toBe(
    'Disabled',
  )
  await selectIncludedPermission('run_command', 'Always ask')
  await selectIncludedPermission('run_command', 'Disabled')
  expect(container.querySelector('output')?.textContent).toBe(source)
})

it.each([
  ['run_command', 'Run shell commands on an attached machine.'],
  ['skill', 'skill'],
  ['send_integration_message', 'send_integration_message'],
])(
  'shows the frontend description or catalog fallback for %s on hover and keyboard focus',
  async (name, description) => {
    await renderAndFlush(<IncludedToolsHarness source={`${includedSource}  ${name}: {}\n`} />)
    click('[data-slot="collapsible-trigger"]')
    const trigger = container.querySelector<HTMLButtonElement>(`[aria-label="About ${name}"]`)
    if (!trigger) throw new Error('Missing description trigger')
    await act(async () => {
      trigger.dispatchEvent(new PointerEvent('pointerover', { bubbles: true }))
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    expect(document.querySelector('[role="tooltip"]')?.textContent).toBe(description)
    await act(async () => {
      trigger.dispatchEvent(new PointerEvent('pointerout', { bubbles: true }))
      trigger.focus()
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    expect(document.querySelector('[role="tooltip"]')?.textContent).toBe(description)
  },
)

function NewAgentDraftHarness({ pool }: { pool?: MachinePoolSummary }) {
  const { form } = useAgentDraft(catalog, pool, undefined, undefined, {
    orgId: 'org-test',
    projectId: 'project-test',
  })
  return (
    <>
      <button
        type="button"
        onClick={() => {
          form.setMachineSources([])
        }}
      >
        Remove default source
      </button>
      <output>{form.yaml}</output>
    </>
  )
}

it('seeds the resolved pool only once, using its current name', async () => {
  const pool = machinePool({ name: 'renamed-hosted-pool', management_kind: 'cluster' })
  await renderAndFlush(<NewAgentDraftHarness pool={pool} />)
  expect(parse(container.querySelector('output')?.textContent ?? '')).toMatchObject({
    machine_sources: [{ machine_pool_name: 'renamed-hosted-pool' }],
    tools: { web_search: { type: 'built_in' } },
  })
  click('button')
  await renderAndFlush(<NewAgentDraftHarness pool={pool} />)
  expect(parse(container.querySelector('output')?.textContent ?? '')).not.toHaveProperty(
    'machine_sources',
  )
})

it('seeds an empty pool source when no project pool is available', async () => {
  await renderAndFlush(<NewAgentDraftHarness />)
  expect(parse(container.querySelector('output')?.textContent ?? '')).toMatchObject({
    machine_sources: [{ machine_pool_name: '' }],
  })
})

function ProjectDefaultsHarness() {
  const defaults = useProjectDefaults('org-test', 'project-test')
  return <output data-ready={defaults.ready}>{defaults.defaultPool?.name ?? ''}</output>
}

it.each(['cluster', 'tenant', 'error'])(
  'only selects a cluster pool after checking later grant pages (%s)',
  async (laterPage) => {
    const first = {
      grant: projectMachinePoolGrant(),
      machine_pool: machinePool({ name: 'first-tenant' }),
    }
    const later = {
      grant: projectMachinePoolGrant(),
      machine_pool: machinePool({ name: 'renamed-hosted', management_kind: 'cluster' }),
    }
    let releasePage: (response: Response) => void = () => undefined
    const nextPage = new Promise<Response>((resolve) => {
      releasePage = resolve
    })
    const requestedCursors: (string | null)[] = []
    Providers = testProviders([
      {
        method: 'GET',
        path: '/api/v1/orgs/org-test/projects/project-test/machine-pool-grants',
        respond: ({ url }) => {
          const cursor = url.searchParams.get('cursor')
          requestedCursors.push(cursor)
          return cursor ? nextPage : Response.json({ data: [first], next_cursor: 'next' })
        },
      },
      {
        method: 'GET',
        path: '/api/v1/orgs/org-test/projects/project-test/model-grants',
        respond: () => jsonResponse({ data: [], next_cursor: null }),
      },
    ])
    await renderAndFlush(<ProjectDefaultsHarness />)
    await vi.waitFor(() => {
      expect(requestedCursors).toEqual([null, 'next'])
    })
    expect(container.querySelector('output')?.dataset.ready).toBe('false')
    expect(container.querySelector('output')?.textContent).toBe('')
    await act(async () => {
      releasePage(
        laterPage === 'error'
          ? jsonResponse({ code: 'internal_error', message: 'Unavailable' }, 500)
          : Response.json({
              data: laterPage === 'cluster' ? [later] : [],
              next_cursor: null,
            }),
      )
      await nextPage
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    await vi.waitFor(() => {
      expect(container.querySelector('output')?.dataset.ready).toBe('true')
    })
    expect(container.querySelector('output')?.textContent).toBe(
      laterPage === 'cluster' ? 'renamed-hosted' : '',
    )
    expect(requestedCursors).toEqual([null, 'next'])
  },
)

it('groups configured machine, skill, and integration tools in one dropdown inside Tools', async () => {
  const onToolsChange = vi.fn()
  await renderAndFlush(
    <AgentConfigToolsField
      catalog={includedCatalog}
      tools={[
        { name: 'run_command', permission: null },
        { name: 'skill', permission: null },
        { name: 'send_integration_message', permission: null },
        { name: 'web_search', permission: null },
      ]}
      onToolsChange={onToolsChange}
    />,
  )
  expect(container.querySelectorAll('[data-slot="collapsible-trigger"]')).toHaveLength(1)
  expect(container.querySelector('[data-slot="collapsible-trigger"]')?.textContent).toBe(
    'Built-in tools',
  )
  expect(container.textContent).toContain('web_search')
  for (const name of ['run_command', 'skill', 'send_integration_message']) {
    expect(container.textContent).not.toContain(name)
    expect(container.querySelector(`[aria-label="Remove ${name}"]`)).toBeNull()
  }
  click('[data-slot="collapsible-trigger"]')
  for (const name of ['run_command', 'skill', 'send_integration_message']) {
    const control = container.querySelector(`[aria-label="${name} permission"]`)
    expect(control).not.toBeNull()
    expect(control?.closest('[data-slot="collapsible-content"]')).not.toBeNull()
    expect(control?.closest('section')?.querySelector('h3')?.textContent).toBe('Tools')
  }
  expect(onToolsChange).not.toHaveBeenCalled()
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
