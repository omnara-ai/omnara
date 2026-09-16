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

const includedCatalog: ToolCatalog = {
  ...catalog,
  built_in_tools: ['run_command', 'skill', 'send_integration_message', ...subagentToolNames].map(
    (name) => ({
      name,
      description: name,
      automatically_added: true,
      default_permission: alwaysAllowProfile.default_permission,
      permission_modes:
        name === 'send_integration_message'
          ? alwaysAllowProfile.permission_modes.slice(0, 1)
          : alwaysAllowProfile.permission_modes,
    }),
  ),
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
          form.setMachineSources([{ ...newMachineSource('machine'), name: 'box' }])
        }}
      >
        Select machine
      </button>
      <button
        type="button"
        onClick={() => {
          form.setSkillIds(['skl_aaaaaaaaaaaaaaaaaaaaaaaaaa'])
        }}
      >
        Select skill
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
          form.reset(createBasicConfigSession(includedSource).initialDraft)
        }}
      >
        Reset draft
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
          form.setSubagents(form.subagents.map((row) => ({ ...row, key: '' })))
        }}
      >
        Clear subagent key
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

it('does not offer the Slack tool when it is absent from the source', async () => {
  const requests: unknown[] = []
  Providers = testProviders([
    {
      method: 'POST',
      path: '/api/v1/orgs/org-test/projects/project-test/agent-configs/tools',
      respond: ({ body }) => {
        requests.push(body)
        return jsonResponse({ tools: [] })
      },
    },
  ])
  await renderAndFlush(<BasicFormHarness />)
  expect(container.querySelector('[data-slot="collapsible-trigger"]')).toBeNull()
  expect(container.querySelector('output')?.textContent).toBe(includedSource)
  expect(requests).toEqual([
    {
      source_format: 'json',
      source: JSON.stringify({ machine_sources: [], skills: [], subagents: {} }),
    },
  ])
})

it.each([true, false])(
  'keeps an explicitly configured Slack tool editable (enabled: %s)',
  async (enabled) => {
    Providers = testProviders([sourceToolsRoute()])
    await renderAndFlush(
      <BasicFormHarness
        source={`${includedSource}  send_integration_message: {enabled: ${enabled}}\n`}
      />,
    )
    await vi.waitFor(() => {
      expect(container.querySelector('[data-slot="collapsible-trigger"]')).not.toBeNull()
    })
    click('[data-slot="collapsible-trigger"]')
    await vi.waitFor(() => {
      expect(
        container.querySelector('[aria-label="send_integration_message permission"]')?.textContent,
      ).toBe(enabled ? 'Always allow' : 'Disabled')
      expect(
        container
          .querySelector('[aria-label="send_integration_message permission"]')
          ?.hasAttribute('disabled'),
      ).toBe(false)
    })
    await selectIncludedPermission('send_integration_message', 'Always allow')
    expect(
      container.querySelector('[aria-label="send_integration_message permission"]'),
    ).not.toBeNull()
    expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
      'tools.send_integration_message',
    )
    await selectIncludedPermission('send_integration_message', 'Disabled')
    expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
      'tools.send_integration_message.enabled',
      false,
    )
  },
)

it('uses backend tool names for source changes while keeping permission edits local', async () => {
  const requests: unknown[] = []
  Providers = testProviders([
    {
      method: 'POST',
      path: '/api/v1/orgs/org-test/projects/project-test/agent-configs/tools',
      respond: ({ body }) => {
        const request = schemas.zResolveAgentConfigToolsRequest.parse(body)
        requests.push(request)
        return jsonResponse({
          tools:
            request.source ===
            JSON.stringify({
              machine_sources: [{ machine_pool_name: 'pool' }],
              skills: [],
              subagents: {},
            })
              ? [
                  {
                    name: 'run_command',
                    enabled: true,
                    permission: { mode: 'always_allow', parameters: {} },
                  },
                ]
              : [],
        })
      },
    },
  ])
  await renderAndFlush(<BasicFormHarness />)
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
  expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
    'tools.run_command',
    { type: 'built_in' },
  )
  expect(container.textContent).not.toContain('create_machine')
  const requestCount = requests.length
  await selectIncludedPermission('run_command', 'Disabled')
  expect(parse(container.querySelector('output')?.textContent ?? '')).toMatchObject({
    tools: { run_command: { enabled: false } },
  })
  expect(requests).toHaveLength(requestCount)
  act(() => {
    ;[...container.querySelectorAll('button')]
      .find((button) => button.textContent === 'Remove pool')
      ?.click()
  })
  await vi.waitFor(() => {
    expect(container.querySelector('[data-slot="collapsible-trigger"]')).toBeNull()
  })
  expect(parse(container.querySelector('output')?.textContent ?? '')).not.toHaveProperty(
    'tools.run_command',
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

function sourceToolsRoute(): FakeRoute {
  return {
    method: 'POST',
    path: '/api/v1/orgs/org-test/projects/project-test/agent-configs/tools',
    respond: ({ body }) => {
      const request = schemas.zResolveAgentConfigToolsRequest.parse(body)
      const names: string[] = []
      if (request.source.includes('machine_pool_name'))
        names.push('create_machine', 'delete_machine')
      if (request.source.includes('machine_pool_name') || request.source.includes('machine_name'))
        names.push('run_command')
      if (request.source.includes('skl_')) names.push('skill')
      if (request.source.includes('"type":"self"') || request.source.includes('"type":"profile"'))
        names.push(...subagentToolNames)
      return toolResponse(names)
    },
  }
}

it('cleans up only tools whose last relevant source was removed', async () => {
  Providers = testProviders([sourceToolsRoute()])
  const source = `${includedSource}  run_command: {enabled: false, permission: {mode: always_ask}}
  delete_machine: {permission: {mode: always_ask}}
machine_sources: [{machine_pool_name: pool}]
skills: [skl_aaaaaaaaaaaaaaaaaaaaaaaaaa]
`
  await renderAndFlush(<BasicFormHarness source={source} />)
  await vi.waitFor(() => {
    expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
      'tools.skill',
    )
  })
  clickLabel('Select machine')
  await vi.waitFor(() => {
    const config: unknown = parse(container.querySelector('output')?.textContent ?? '')
    expect(config).not.toHaveProperty('tools.delete_machine')
    expect(config).not.toHaveProperty('tools.create_machine')
    expect(config).toHaveProperty('tools.run_command.enabled', false)
    expect(config).toHaveProperty('tools.skill')
  })
  clickLabel('Remove skill')
  await vi.waitFor(() => {
    expect(parse(container.querySelector('output')?.textContent ?? '')).not.toHaveProperty(
      'tools.skill',
    )
  })
  clickLabel('Remove pool')
  await vi.waitFor(() => {
    expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty('tools', {
      web_search: { permission: { mode: 'always_ask' } },
    })
  })
  clickLabel('Select pool')
  await vi.waitFor(() => {
    expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
      'tools.run_command',
      { type: 'built_in' },
    )
  })
  clickLabel('Reset draft')
  await vi.waitFor(() => {
    expect(container.querySelector('output')?.textContent).toBe(includedSource)
  })
})

it('adds subagent defaults, preserves edits, and cleans up after incomplete or removed subagents', async () => {
  Providers = testProviders([sourceToolsRoute()])
  await renderAndFlush(
    <BasicFormHarness
      source={`${includedSource}  spawn_agent: {enabled: false}
  read_agent: {permission: {mode: always_ask}}
subagents: {worker: {type: self}}
`}
    />,
  )
  await vi.waitFor(() => {
    expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
      'tools.list_agents',
    )
  })
  await vi.waitFor(() => {
    expect(container.querySelector('[data-slot="collapsible-trigger"]')).not.toBeNull()
  })
  click('[data-slot="collapsible-trigger"]')
  expect(container.querySelector('[aria-label="spawn_agent permission"]')?.textContent).toBe(
    'Disabled',
  )
  expect(container.querySelector('[aria-label="read_agent permission"]')?.textContent).toBe(
    'Always ask',
  )
  await selectIncludedPermission('list_agents', 'Disabled')
  expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
    'tools.list_agents.enabled',
    false,
  )
  clickLabel('Clear subagent key')
  await vi.waitFor(() => {
    expect(container.querySelector('output')?.getAttribute('data-pending')).toBe('false')
    const config: unknown = parse(container.querySelector('output')?.textContent ?? '')
    expect(config).toHaveProperty('tools.spawn_agent.enabled', false)
    expect(config).toHaveProperty('tools.read_agent.permission.mode', 'always_ask')
    expect(config).toHaveProperty('tools.list_agents.enabled', false)
  })
  clickLabel('Select subagent')
  await vi.waitFor(() => {
    expect(container.querySelector('output')?.getAttribute('data-pending')).toBe('false')
    expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
      'tools.list_agents.enabled',
      false,
    )
  })
  clickLabel('Clear subagent key')
  clickLabel('Remove subagent')
  await vi.waitFor(() => {
    expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty('tools', {
      web_search: { permission: { mode: 'always_ask' } },
    })
    expect(container.querySelector('output')?.getAttribute('data-error')).toBe('false')
  })
  clickLabel('Incomplete subagent')
  await vi.waitFor(() => {
    expect(container.querySelector('output')?.getAttribute('data-pending')).toBe('false')
    expect(container.querySelector('output')?.getAttribute('data-error')).toBe('false')
  })
  clickLabel('Select subagent')
  await vi.waitFor(() => {
    const config: unknown = parse(container.querySelector('output')?.textContent ?? '')
    for (const name of subagentToolNames) {
      expect(config).toHaveProperty(['tools', name], { type: 'built_in' })
    }
  })
})

it.each([
  {
    source: 'machine_sources: [{machine_pool_name: pool}]',
    tool: 'run_command',
    marker: 'machine_pool_name',
    remove: 'Remove pool',
  },
  {
    source: 'subagents: {worker: {type: self}}',
    tool: 'spawn_agent',
    marker: 'worker',
    remove: 'Remove subagent',
  },
])(
  'removes saved $tool even when its source is removed before its first lookup completes',
  async (test) => {
    let release: (response: Response) => void = () => undefined
    const pending = new Promise<Response>((resolve) => {
      release = resolve
    })
    Providers = testProviders([
      {
        ...sourceToolsRoute(),
        respond: ({ body }) =>
          schemas.zResolveAgentConfigToolsRequest.parse(body).source.includes(test.marker)
            ? pending.then((response) => response.clone())
            : toolResponse([]),
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
    clickLabel(test.remove)
    act(() => {
      release(toolResponse([test.tool]))
    })
    await vi.waitFor(() => {
      expect(container.querySelector('output')?.getAttribute('data-pending')).toBe('false')
      expect(container.querySelector('output')?.getAttribute('data-error')).toBe('false')
      expect(parse(container.querySelector('output')?.textContent ?? '')).not.toHaveProperty(
        `tools.${test.tool}`,
      )
    })
  },
)
