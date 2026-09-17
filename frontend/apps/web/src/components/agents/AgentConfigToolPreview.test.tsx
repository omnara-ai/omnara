/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import {
  createOmnaraClient,
  schemas,
  type ToolCatalog,
  type ToolPermissionProfile,
} from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act, createContext, type ReactNode, useContext } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import { parse } from 'yaml'

import { AgentConfigBasicForm } from '@/components/agents/AgentConfigBasicForm'
import { newSubagent } from '@/components/agents/agentConfigSubagents'
import {
  createBasicConfigSession,
  newMachineSource,
  useAgentBuilderForm,
} from '@/components/agents/useAgentBuilderForm'
import { ActiveOrgContext } from '@/lib/active-org-context'
import { fakeApi, type FakeRoute, jsonResponse } from '@/test/fake-api'
import { currentUserOrg } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'

const TestContentContext = createContext<ReactNode>(null)
function TestContent() {
  return useContext(TestContentContext)
}
const testRouter = createRouter({
  routeTree: createRootRoute({ component: TestContent }),
  history: createMemoryHistory(),
})

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

const subagentToolNames = ['spawn_agent', 'read_agent', 'list_agents']
const channelToolNames = [
  'list_channels',
  'get_channel',
  'set_current_channel',
  'send_channel_message',
  'read_channel',
]

const includedCatalog: ToolCatalog = {
  ...catalog,
  built_in_tools: [
    ...catalog.built_in_tools,
    ...[
      'run_command',
      'create_machine',
      'delete_machine',
      'skill',
      'tool_search',
      'read_file',
      'search_files',
      ...subagentToolNames,
    ].map((name) => ({
      name,
      description: name,
      configurable: true,
      implicit: true,
      default_permission: alwaysAllowProfile.default_permission,
      permission_modes:
        name === 'tool_search'
          ? alwaysAllowProfile.permission_modes.slice(0, 1)
          : alwaysAllowProfile.permission_modes,
    })),
    ...channelToolNames.map((name) => ({
      name,
      description: name,
      configurable: false,
      implicit: true,
      default_permission: alwaysAllowProfile.default_permission,
      permission_modes: alwaysAllowProfile.permission_modes.slice(0, 1),
    })),
  ],
}

let container: HTMLDivElement
let root: Root
let restoreActEnvironment: () => void

beforeAll(async () => {
  restoreActEnvironment = enableReactActEnvironment()
  await testRouter.load()
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
      method: 'GET',
      path: '/api/v1/orgs/org-test/projects',
      respond: () => jsonResponse({ data: [], next_cursor: null }),
    },
    {
      method: 'GET',
      path: '/api/v1/orgs/org-test/projects/project-test/machine-pool-grants',
      respond: () => jsonResponse({ data: [], next_cursor: null }),
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
  return function Providers({ children }: { children: ReactNode }) {
    return (
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <ActiveOrgContext.Provider
            value={{ orgs: [activeOrg], activeOrg, setActiveOrgId: () => undefined }}
          >
            <TestContentContext value={<form>{children}</form>}>
              <RouterProvider router={testRouter} />
            </TestContentContext>
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

const includedSource = `instruction: Test included tools.
model:
  provider_config: openai
  name: primary
tools:
  web_search:
    permission:
      mode: always_ask
`

function BasicFormHarness({ source = includedSource }: { source?: string }) {
  const form = useAgentBuilderForm(createBasicConfigSession(source), undefined, {
    orgId: 'org-test',
    projectId: 'project-test',
  })
  return (
    <>
      <button
        type="button"
        onClick={() => {
          form.setMcpServers([])
        }}
      >
        Remove MCP
      </button>
      <button
        type="button"
        onClick={() => {
          form.setTools(form.tools.filter((tool) => tool.name !== 'web_search'))
        }}
      >
        Remove web search
      </button>
      <button
        type="button"
        onClick={() => {
          form.setMachineSources([{ ...newMachineSource('machine'), name: 'box' }])
        }}
      >
        Select machine
      </button>
      <button
        type="button"
        onClick={() => {
          form.setSkillIds([])
        }}
      >
        Remove skill
      </button>
      <button
        type="button"
        onClick={() => {
          form.setSubagents([{ ...newSubagent(), key: 'worker', type: 'self' }])
        }}
      >
        Select subagent
      </button>
      <button
        type="button"
        onClick={() => {
          form.setSubagents([newSubagent()])
        }}
      >
        Incomplete subagent
      </button>
      <button
        type="button"
        onClick={() => {
          form.setSubagents([])
        }}
      >
        Remove subagent
      </button>
      <button
        type="button"
        onClick={() => {
          form.setMachineSources([{ ...newMachineSource('pool'), name: 'pool' }])
        }}
      >
        Select pool
      </button>
      <button
        type="button"
        onClick={() => {
          form.setMachineSources([])
        }}
      >
        Remove pool
      </button>
      <AgentConfigBasicForm orgId="org-test" projectId="project-test" form={form} />
      <output data-pending={form.toolsPending} data-error={form.toolsError}>
        {form.yaml}
      </output>
    </>
  )
}

function click(selector: string) {
  const button = container.querySelector<HTMLButtonElement>(selector)
  if (!button) throw new Error(`Missing ${selector}`)
  act(() => {
    button.click()
  })
}

async function selectIncludedPermission(name: string, label: string) {
  await act(async () => {
    container
      .querySelector(`[aria-label="${name} permission"]`)
      ?.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(
    (item) => item.textContent === label,
  )
  if (!option) throw new Error(`Missing ${label} option`)
  act(() => {
    option.click()
  })
}

it.each([{ names: [] }, { names: channelToolNames }])(
  'keeps binding-managed channel previews out of the source editor ($names)',
  async ({ names }) => {
    const requests: unknown[] = []
    Providers = testProviders([
      {
        method: 'POST',
        path: '/api/v1/orgs/org-test/projects/project-test/agent-configs/tools',
        respond: ({ body }) => {
          requests.push(body)
          return toolResponse(names)
        },
      },
    ])
    await renderAndFlush(<BasicFormHarness />)
    await vi.waitFor(() => {
      expect(requests).toHaveLength(1)
      expect(container.querySelector('output')?.getAttribute('data-pending')).toBe('false')
    })
    expect(container.querySelector('[data-slot="collapsible-trigger"]')).toBeNull()
    expect(container.querySelector<HTMLButtonElement>('[aria-label="Add tools"]')?.disabled).toBe(
      true,
    )
    for (const name of channelToolNames) {
      expect(container.querySelector(`[aria-label="${name} permission"]`)).toBeNull()
      expect(container.textContent).not.toContain(name)
    }
    expect(container.querySelector('output')?.textContent).toBe(includedSource)
    expect(requests).toEqual([
      {
        source_format: 'json',
        source: JSON.stringify({
          tools: { web_search: { type: 'built_in', permission: { mode: 'always_ask' } } },
          mcp: {},
          machine_sources: [],
          skills: [],
          subagents: {},
        }),
      },
    ])
  },
)

it('includes configured MCP servers in previews and removes them after deletion', async () => {
  const requests: unknown[] = []
  Providers = testProviders([
    {
      ...previewToolsRoute(() => []),
      respond: ({ body }) => {
        const request = schemas.zResolveAgentConfigToolsRequest.parse(body)
        requests.push(JSON.parse(request.source))
        return toolResponse([])
      },
    },
  ])
  await renderAndFlush(
    <BasicFormHarness
      source={`${includedSource}mcp: {docs: {url: https://example.com/mcp, default_enabled: false}}\n`}
    />,
  )
  await vi.waitFor(() => {
    expect(requests.at(-1)).toHaveProperty('mcp', {
      docs: { url: 'https://example.com/mcp', default_enabled: false },
    })
  })
  clickLabel('Remove MCP')
  await vi.waitFor(() => {
    expect(requests.at(-1)).toHaveProperty('mcp', {})
  })
})

it('displays backend defaults without saving them and preserves user overrides', async () => {
  let preview: string[] = []
  Providers = testProviders([previewToolsRoute(() => preview)])
  await renderAndFlush(<BasicFormHarness />)
  preview = ['run_command']
  act(() => {
    ;[...container.querySelectorAll('button')]
      .find((button) => button.textContent === 'Select pool')
      ?.click()
  })
  await vi.waitFor(() => {
    expect(container.textContent).toContain('Other tools')
  })
  act(() => {
    ;[...container.querySelectorAll('button')]
      .find((button) => button.textContent === 'Other tools')
      ?.click()
  })
  expect(container.querySelector('[aria-label="run_command permission"]')?.textContent).toBe(
    'Always allow',
  )
  expect(parse(container.querySelector('output')?.textContent ?? '')).not.toHaveProperty(
    'tools.run_command',
  )
  expect(container.textContent).not.toContain('create_machine')
  await selectIncludedPermission('run_command', 'Always ask')
  expect(parse(container.querySelector('output')?.textContent ?? '')).toMatchObject({
    tools: { run_command: { permission: { mode: 'always_ask' } } },
  })
  preview = []
  act(() => {
    ;[...container.querySelectorAll('button')]
      .find((button) => button.textContent === 'Remove pool')
      ?.click()
  })
  await vi.waitFor(() => {
    expect(container.querySelector('output')?.getAttribute('data-pending')).toBe('false')
  })
  expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
    'tools.run_command.permission.mode',
    'always_ask',
  )
})

it('shows a preview failure and lets the user retry without editing the draft', async () => {
  let attempts = 0
  Providers = testProviders([
    {
      method: 'POST',
      path: '/api/v1/orgs/org-test/projects/project-test/agent-configs/tools',
      respond: () => {
        attempts += 1
        return attempts === 1
          ? jsonResponse({ code: 'internal_error', message: 'Unavailable' }, 500)
          : jsonResponse({ tools: [] })
      },
    },
  ])
  await renderAndFlush(<BasicFormHarness />)
  await vi.waitFor(() => {
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      'Couldn’t load other tools',
    )
  })
  click('[role="alert"] button')
  await vi.waitFor(() => {
    expect(attempts).toBe(2)
    expect(container.querySelector('[role="alert"]')).toBeNull()
  })
  expect(container.querySelector('[data-slot="collapsible-trigger"]')).toBeNull()
})

function clickLabel(label: string) {
  const button = [...container.querySelectorAll('button')].find(
    (item) => item.textContent === label,
  )
  if (!button) throw new Error(`Missing ${label}`)
  act(() => {
    button.click()
  })
}

function toolResponse(names: string[]) {
  return jsonResponse({
    tools: names.map((name) => ({
      name,
      enabled: true,
      permission: { mode: 'always_allow', parameters: {} },
    })),
  })
}

function previewToolsRoute(names: () => string[]): FakeRoute {
  return {
    method: 'POST',
    path: '/api/v1/orgs/org-test/projects/project-test/agent-configs/tools',
    respond: () => toolResponse(names()),
  }
}

it('displays retrieval defaults without changing source and saves only edited overrides', async () => {
  Providers = testProviders([previewToolsRoute(() => ['read_file', 'search_files'])])
  await renderAndFlush(<BasicFormHarness />)
  await vi.waitFor(() => {
    expect(container.querySelector('[data-slot="collapsible-trigger"]')).not.toBeNull()
    expect(container.querySelector('output')?.getAttribute('data-pending')).toBe('false')
  })
  expect(container.querySelector('output')?.textContent).toBe(includedSource)
  clickLabel('Other tools')
  expect(container.querySelector('[aria-label="read_file permission"]')?.textContent).toBe(
    'Always allow',
  )
  await selectIncludedPermission('read_file', 'Disabled')
  await vi.waitFor(() => {
    expect(container.querySelector('[aria-label="search_files permission"]')).not.toBeNull()
  })
  await selectIncludedPermission('search_files', 'Always ask')
  await vi.waitFor(() => {
    const saved = container.querySelector('output')?.textContent ?? ''
    expect(parse(saved)).toHaveProperty('tools.read_file.enabled', false)
    expect(parse(saved)).toHaveProperty('tools.search_files.permission.mode', 'always_ask')
    expect(
      createBasicConfigSession(saved).initialDraft?.tools.find((tool) => tool.name === 'read_file')
        ?.enabled,
    ).toBe(false)
  })
  clickLabel('Remove web search')
  await vi.waitFor(() => {
    expect(container.querySelector('output')?.getAttribute('data-pending')).toBe('false')
  })
  expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
    'tools.read_file.enabled',
    false,
  )
  expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
    'tools.search_files.permission.mode',
    'always_ask',
  )
})

it('updates resource defaults without writing or removing explicit entries', async () => {
  let preview = ['create_machine', 'skill']
  const requests: unknown[] = []
  Providers = testProviders([
    {
      ...previewToolsRoute(() => preview),
      respond: ({ body }) => {
        const request = schemas.zResolveAgentConfigToolsRequest.parse(body)
        requests.push(JSON.parse(request.source))
        return toolResponse(preview)
      },
    },
  ])
  const source = `${includedSource}  run_command: {enabled: false, permission: {mode: always_ask}}
  delete_machine: {permission: {mode: always_ask}}
machine_sources: [{machine_pool_name: pool}]
skills: [skl_aaaaaaaaaaaaaaaaaaaaaaaaaa]
`
  await renderAndFlush(<BasicFormHarness source={source} />)
  await vi.waitFor(() => {
    expect(container.querySelector('output')?.getAttribute('data-pending')).toBe('false')
  })
  clickLabel('Other tools')
  await vi.waitFor(() => {
    expect(container.querySelector('[aria-label="create_machine permission"]')).not.toBeNull()
  })
  expect(container.querySelector('[aria-label="skill permission"]')).not.toBeNull()
  expect(container.querySelector('output')?.textContent).toBe(source)
  preview = ['skill']
  clickLabel('Select machine')
  await vi.waitFor(() => {
    expect(container.querySelector('[aria-label="create_machine permission"]')).toBeNull()
    expect(requests.at(-1)).toMatchObject({
      machine_sources: [{ machine_name: 'box' }],
      skills: ['skl_aaaaaaaaaaaaaaaaaaaaaaaaaa'],
    })
  })
  expect(container.querySelector('[aria-label="delete_machine permission"]')?.textContent).toBe(
    'Always ask',
  )
  preview = []
  clickLabel('Remove skill')
  await vi.waitFor(() => {
    expect(container.querySelector('[aria-label="skill permission"]')).toBeNull()
  })
  clickLabel('Remove pool')
  await vi.waitFor(() => {
    expect(container.querySelector('output')?.getAttribute('data-pending')).toBe('false')
  })
  expect(requests.at(-1)).toMatchObject({ machine_sources: [], skills: [] })
  expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty('tools', {
    web_search: { permission: { mode: 'always_ask' } },
    run_command: { enabled: false, permission: { mode: 'always_ask' } },
    delete_machine: { permission: { mode: 'always_ask' } },
  })
})

it.each(['Always ask', 'Disabled'])('removes spawn_agent override: %s', async (mode) => {
  let preview = ['spawn_agent', 'list_agents']
  Providers = testProviders([previewToolsRoute(() => preview)])
  await renderAndFlush(
    <BasicFormHarness
      source={`${includedSource}  read_agent: {enabled: false}\nsubagents: {worker: {type: self}}\n`}
    />,
  )
  await vi.waitFor(() => {
    expect(container.querySelector('output')?.getAttribute('data-pending')).toBe('false')
  })
  await vi.waitFor(() => {
    expect(container.querySelector('[data-slot="collapsible-trigger"]')).not.toBeNull()
  })
  clickLabel('Other tools')
  await vi.waitFor(() => {
    expect(container.querySelector('[aria-label="spawn_agent permission"]')?.textContent).toBe(
      'Always allow',
    )
  })
  expect(parse(container.querySelector('output')?.textContent ?? '')).not.toHaveProperty(
    'tools.spawn_agent',
  )
  await selectIncludedPermission('spawn_agent', mode)
  expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
    'tools.spawn_agent',
  )
  expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
    'tools.read_agent.enabled',
    false,
  )
  preview = []
  clickLabel('Remove subagent')
  expect(parse(container.querySelector('output')?.textContent ?? '')).not.toHaveProperty(
    'tools.spawn_agent',
  )
  await vi.waitFor(() => {
    expect(container.querySelector('[aria-label="spawn_agent permission"]')).toBeNull()
    expect(container.querySelector('[aria-label="list_agents permission"]')).toBeNull()
  })
  expect(container.querySelector('[aria-label="read_agent permission"]')?.textContent).toBe(
    'Disabled',
  )
  clickLabel('Incomplete subagent')
  await vi.waitFor(() => {
    expect(container.querySelector('output')?.getAttribute('data-error')).toBe('false')
  })
  preview = ['spawn_agent', 'list_agents']
  clickLabel('Select subagent')
  await vi.waitFor(() => {
    expect(container.querySelector('[aria-label="spawn_agent permission"]')).not.toBeNull()
  })
  expect(container.querySelector('[aria-label="read_agent permission"]')?.textContent).toBe(
    'Disabled',
  )
})

it.each([
  {
    source: 'machine_sources: [{machine_pool_name: pool}]',
    tool: 'run_command',
    remove: 'Remove pool',
  },
  {
    source: 'subagents: {worker: {type: self}}',
    tool: 'spawn_agent',
    remove: 'Remove subagent',
  },
])(
  'handles explicit $tool when its source is removed before its first lookup completes',
  async (test) => {
    let release: (response: Response) => void = () => undefined
    const pending = new Promise<Response>((resolve) => {
      release = resolve
    })
    let removed = false
    Providers = testProviders([
      {
        ...previewToolsRoute(() => []),
        respond: () => (removed ? toolResponse([]) : pending.then((response) => response.clone())),
      },
    ])
    await renderAndFlush(
      <BasicFormHarness
        source={`${includedSource}  ${test.tool}: {enabled: false}
${test.source}
`}
      />,
    )
    expect(container.querySelector('output')?.getAttribute('data-pending')).toBe('true')
    removed = true
    clickLabel(test.remove)
    act(() => {
      release(toolResponse([test.tool]))
    })
    await vi.waitFor(() => {
      expect(container.querySelector('output')?.getAttribute('data-pending')).toBe('false')
      expect(container.querySelector('output')?.getAttribute('data-error')).toBe('false')
      const source: unknown = parse(container.querySelector('output')?.textContent ?? '')
      if (test.tool === 'spawn_agent') {
        expect(source).not.toHaveProperty('tools.spawn_agent')
      } else {
        expect(source).toHaveProperty(`tools.${test.tool}.enabled`, false)
      }
    })
  },
)
