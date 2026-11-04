/** @vitest-environment happy-dom */

import { OmnaraClientProvider, useAgentConfigTools } from '@omnara/react'
import { createOmnaraClient, schemas, type ToolCatalog } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  createContext,
  createRef,
  type ReactNode,
  type Ref,
  StrictMode,
  useContext,
  useImperativeHandle,
} from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import { isNode, parse, parseDocument } from 'yaml'
import { z } from 'zod'

import { configPath } from '@/components/agents/agentConfigCompatibility'
import { AgentConfigEditorFields } from '@/components/agents/AgentConfigEditor'
import { AgentConfigToolsSection } from '@/components/agents/AgentConfigToolsSection'
import { agentTemplates } from '@/components/agents/agentTemplates'
import customFixture from '@/components/agents/fixtures/inline-custom-tools.yaml?raw'
import { savePendingMcpBuilderOAuth } from '@/components/agents/pendingMcpBuilderOAuth'
import {
  createBasicConfigSession,
  useAgentBuilderForm,
} from '@/components/agents/useAgentBuilderForm'
import {
  type AgentConfigEditorState,
  useAgentConfigEditor,
} from '@/components/agents/useAgentConfigEditor'
import { useAgentDraft } from '@/components/agents/useAgentDraft'
import { ActiveOrgContext } from '@/lib/active-org-context'
import { fakeApi, type FakeRoute, jsonResponse } from '@/test/fake-api'
import { currentUserOrg } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, field } from '@/test/secret-editor'

const includedSource = 'instruction: Help.\nmodel: {provider_config: openai, name: primary}\n'
let container: HTMLDivElement
let root: Root
let previousUrl: string
let restoreActEnvironment: () => void
let Providers: ReturnType<typeof testProviders>

beforeAll(() => {
  restoreActEnvironment = enableReactActEnvironment()
})
afterAll(() => {
  restoreActEnvironment()
})
beforeEach(() => {
  previousUrl = window.location.href
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  window.history.replaceState(null, '', previousUrl)
  window.sessionStorage.clear()
})

const TestContentContext = createContext<ReactNode>(null)
function TestContent() {
  return useContext(TestContentContext)
}

function testProviders(routes: FakeRoute[]) {
  const api = fakeApi([
    ...routes,
    {
      method: 'GET',
      path: '/api/v1/orgs/org-test/projects',
      respond: () => jsonResponse({ data: [], next_cursor: null }),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const router = createRouter({
    routeTree: createRootRoute({ component: TestContent }),
    history: createMemoryHistory(),
  })
  return function Providers({ children }: { children: ReactNode }) {
    return (
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <ActiveOrgContext
            value={{
              activeOrg: currentUserOrg({ id: 'org-test', name: 'Example' }),
              orgs: [],
              setActiveOrgId: () => undefined,
            }}
          >
            <TestContentContext value={children}>
              <RouterProvider router={router} />
            </TestContentContext>
          </ActiveOrgContext>
        </QueryClientProvider>
      </OmnaraClientProvider>
    )
  }
}

async function renderAndFlush(node: ReactNode) {
  await act(async () => {
    root.render(
      <Providers>
        <StrictMode>{node}</StrictMode>
      </Providers>,
    )
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

function clickLabel(label: string) {
  act(() => {
    button(label).click()
  })
}

function PreviewModeHarness() {
  const scope = { orgId: 'org-test', projectId: 'project-test' }
  const editor = useAgentConfigEditor({
    ...scope,
    source: includedSource,
    canManage: true,
    preferredMode: 'builder',
    onModeChange: () => undefined,
    onDirtyChange: () => undefined,
  })
  const draft = useAgentDraft(undefined, undefined, undefined, undefined, scope)
  return (
    <>
      <button
        type="button"
        onClick={() => {
          editor.form.setInstruction('Changed instruction.')
        }}
      >
        Edit instruction
      </button>
      <button
        type="button"
        onClick={() => {
          editor.switchMode('yaml')
        }}
      >
        Editor YAML
      </button>
      <button
        type="button"
        onClick={() => {
          draft.switchMode('yaml')
        }}
      >
        Draft YAML
      </button>
      <output
        data-blocked={editor.saveBlocked}
        data-pending={editor.form.toolsPending}
        data-error={editor.form.toolsError}
        data-editor-mode={editor.mode.mode}
        data-draft-mode={draft.mode.mode}
      >
        {editor.yaml}
      </output>
    </>
  )
}

it.each(['pending', 'error'])(
  'does not block saving or YAML switches on a %s preview',
  async (state) => {
    let release: (response: Response) => void = () => undefined
    const pending = new Promise<Response>((resolve) => {
      release = resolve
    })
    Providers = testProviders([
      {
        method: 'POST',
        path: '/api/v1/orgs/org-test/projects/project-test/agent-configs/tools',
        respond: () =>
          state === 'error'
            ? jsonResponse({ code: 'internal_error', message: 'Unavailable' }, 500)
            : pending.then((response) => response.clone()),
      },
    ])
    await renderAndFlush(<PreviewModeHarness />)
    await vi.waitFor(() => {
      expect(container.querySelector('output')?.getAttribute(`data-${state}`)).toBe('true')
    })
    clickLabel('Edit instruction')
    expect(container.querySelector('output')?.getAttribute('data-blocked')).toBe('false')
    clickLabel('Editor YAML')
    clickLabel('Draft YAML')
    expect(container.querySelector('output')?.getAttribute('data-editor-mode')).toBe('yaml')
    expect(container.querySelector('output')?.getAttribute('data-draft-mode')).toBe('yaml')
    expect(container.querySelector('output')?.getAttribute('data-blocked')).toBe('false')
    expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
      'instruction',
      'Changed instruction.',
    )
    await act(async () => {
      release(jsonResponse({ tools: [] }))
      await pending
    })
  },
)

function SubagentPreviewHarness({ source }: { source: string }) {
  const form = useAgentBuilderForm(createBasicConfigSession(source), undefined, {
    orgId: 'org-test',
    projectId: 'project-test',
  })
  return (
    <>
      {' '}
      <button
        type="button"
        onClick={() => {
          form.setSubagents(
            form.subagents.map((row) => ({
              ...row,
              instructionAppend: `${row.instructionAppend} More detail.`,
              description: 'Updated description',
              modelOverride: { provider_config: 'anthropic', name: 'another-model' },
            })),
          )
        }}
      >
        Edit subagent details
      </button>
      <button
        type="button"
        onClick={() => {
          form.setSubagents([])
        }}
      >
        Remove subagents
      </button>
      <output data-pending={form.toolsPending} data-blocked={form.blocked}>
        {form.yaml}
      </output>
    </>
  )
}

it('does not refetch tool previews for subagent instruction, description, or model edits', async () => {
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
  await renderAndFlush(
    <SubagentPreviewHarness
      source={`${includedSource}subagents: {worker: {type: profile, profile: helper}}\n`}
    />,
  )
  await vi.waitFor(() => {
    expect(container.querySelector('output')?.getAttribute('data-pending')).toBe('false')
  })
  const request = schemas.zResolveAgentConfigToolsRequest.parse(requests[0])
  expect(request.source).toHaveProperty('subagents.worker', {
    type: 'profile',
    profile: 'helper',
  })
  clickLabel('Edit subagent details')
  clickLabel('Edit subagent details')
  expect(requests).toHaveLength(1)
  expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
    'subagents.worker.instruction.append',
    ' More detail. More detail.',
  )
})

it.each([
  ['2', true],
  ['9', false],
])('keeps max_depth %s only if valid when the last subagent is removed', async (maxDepth, kept) => {
  advancedProviders()
  await renderAndFlush(
    <SubagentPreviewHarness
      source={`${includedSource}subagents: {worker: {type: self}}\nmax_depth: ${maxDepth}\n`}
    />,
  )
  clickLabel('Remove subagents')
  const yaml = container.querySelector('output')?.textContent ?? ''
  expect(yaml.includes(`max_depth: ${maxDepth}`)).toBe(kept)
  expect(container.querySelector('output')?.getAttribute('data-blocked')).toBe('false')
})

function ToolPreviewHarness({ source }: { source: string }) {
  const query = useAgentConfigTools('org-test', 'project-test', {
    source,
    source_format: 'yaml',
  })
  return (
    <output data-status={query.status} data-fetching={query.isFetching}>
      {query.data?.tools.map((tool) => tool.name).join(',')}
    </output>
  )
}

function previewResponse(names: string[]) {
  return jsonResponse({
    tools: names.map((name) => ({
      name,
      enabled: true,
      permission: { mode: 'always_allow', parameters: {} },
    })),
  })
}

it('does not seed a new editor from another editor in the same project', async () => {
  const original = 'machine_sources: [{machine_pool_name: pool}]'
  Providers = testProviders([
    {
      method: 'POST',
      path: '/api/v1/orgs/org-test/projects/project-test/agent-configs/tools',
      respond: ({ body }) =>
        schemas.zResolveAgentConfigToolsRequest.parse(body).source === original
          ? previewResponse(['run_command'])
          : jsonResponse({ code: 'internal_error', message: 'Unavailable' }, 500),
    },
  ])
  await renderAndFlush(<ToolPreviewHarness key="first" source={original} />)
  await vi.waitFor(() => {
    expect(container.textContent).toBe('run_command')
  })
  await renderAndFlush(<ToolPreviewHarness key="second" source="tools: {}" />)
  await vi.waitFor(() => {
    expect(container.querySelector('output')?.getAttribute('data-status')).toBe('error')
  })
  expect(container.textContent).toBe('')
})

it('replaces obsolete fallback entries while retaining fetched previews and the displayed order', async () => {
  const original = 'tools: {}'
  const failed = 'machine_sources: [{machine_pool_name: pool}]'
  const updated = `${failed}\ntools: {run_command: {permission: {mode: always_ask}}}`
  const next = 'tools: {web_search: {}}'
  const requests: string[] = []
  let release: (response: Response) => void = () => undefined
  const pending = new Promise<Response>((resolve) => {
    release = resolve
  })
  let fail = true
  Providers = testProviders([
    {
      method: 'POST',
      path: '/api/v1/orgs/org-test/projects/project-test/agent-configs/tools',
      respond: ({ body }) => {
        const source = z.string().parse(schemas.zResolveAgentConfigToolsRequest.parse(body).source)
        requests.push(source)
        if (source === original) return previewResponse(['storage_read'])
        if (source === updated) return previewResponse(['storage_read', 'run_command'])
        if (source === failed && fail) {
          return jsonResponse({ code: 'internal_error', message: 'Unavailable' }, 500)
        }
        return pending.then((response) => response.clone())
      },
    },
  ])
  await renderAndFlush(<ToolPreviewHarness source={original} />)
  await vi.waitFor(() => {
    expect(container.textContent).toBe('storage_read')
  })
  await renderAndFlush(<ToolPreviewHarness source={failed} />)
  await vi.waitFor(() => {
    expect(container.querySelector('output')?.getAttribute('data-status')).toBe('error')
  })
  expect(container.textContent).toBe('storage_read')
  await renderAndFlush(<ToolPreviewHarness source={updated} />)
  await vi.waitFor(() => {
    expect(container.textContent).toBe('storage_read,run_command')
  })
  fail = false
  await renderAndFlush(<ToolPreviewHarness source={failed} />)
  expect(container.querySelector('output')?.getAttribute('data-fetching')).toBe('true')
  expect(container.textContent).toBe('storage_read,run_command')
  await renderAndFlush(<ToolPreviewHarness source={original} />)
  expect(container.textContent).toBe('storage_read')
  expect(requests.filter((source) => source === original)).toHaveLength(1)
  await renderAndFlush(<ToolPreviewHarness source={next} />)
  expect(container.querySelector('output')?.getAttribute('data-fetching')).toBe('true')
  expect(container.textContent).toBe('storage_read')
  await act(async () => {
    release(previewResponse([]))
    await pending
  })
  await vi.waitFor(() => {
    expect(container.querySelector('output')?.getAttribute('data-fetching')).toBe('false')
    expect(container.textContent).toBe('')
  })
})

function AdvancedEditorHarness({
  source,
  state,
  fullEditor,
}: {
  source: string
  state: Ref<AgentConfigEditorState>
  fullEditor: boolean
}) {
  const editor = useAgentConfigEditor({
    orgId: 'org-test',
    projectId: 'project-test',
    source,
    canManage: true,
    preferredMode: 'builder',
    onModeChange: () => undefined,
    onDirtyChange: () => undefined,
  })
  useImperativeHandle(state, () => editor)
  return fullEditor ? (
    <AgentConfigEditorFields
      editor={editor}
      orgId="org-test"
      projectId="project-test"
      yamlFieldId="test-yaml"
      yamlFieldClassName=""
    />
  ) : (
    <AgentConfigToolsSection catalog={customCatalog} form={editor.form} />
  )
}

const customPermissions = {
  default_permission: { mode: 'always_allow', parameters: {} },
  permission_modes: ['always_allow', 'always_ask'].map((name) => ({
    name,
    label: name,
    description: '',
    parameters_schema: {},
  })),
}
const customCatalog: ToolCatalog = {
  built_in_tools: [{ name: 'web_search', description: 'Search.', ...customPermissions }],
  custom_tool_permissions: customPermissions,
  mcp_tool_permissions: customPermissions,
}

function advancedProviders() {
  Providers = testProviders([
    {
      method: 'POST',
      path: '/api/v1/orgs/org-test/projects/project-test/agent-configs/tools',
      respond: () => jsonResponse({ tools: [] }),
    },
  ])
}

async function renderAdvancedEditor(source = customFixture, fullEditor = false) {
  advancedProviders()
  const state = createRef<AgentConfigEditorState>()
  await renderAndFlush(
    <AdvancedEditorHarness source={source} state={state} fullEditor={fullEditor} />,
  )
  return () => {
    if (!state.current) throw new Error('Missing editor')
    return state.current
  }
}

function editYaml(
  editor: () => Pick<AgentConfigEditorState, 'switchMode' | 'dispatchMode' | 'builderYaml'>,
  yaml: string,
) {
  act(() => {
    editor().switchMode('yaml')
    editor().dispatchMode({ type: 'editor-yaml-changed', yaml, builderYaml: editor().builderYaml })
  })
}

function diagnostics(editor: AgentConfigEditorState) {
  return editor.compatibility
    .map((issue) => `${configPath(issue.path)}: ${issue.message}`)
    .join('; ')
}

it('shows normal Builder controls and every custom tool without a whole-document warning', async () => {
  const editor = await renderAdvancedEditor(customFixture, true)
  expect(editor().showBuilder).toBe(true)
  expect(container.querySelector('#agent-config-basic-instruction')).not.toBeNull()
  expect(container.textContent).not.toContain('editable as YAML only')
  const customNames = Object.entries(
    schemas.zAgentConfigDefinition.parse(parse(customFixture)).tools ?? {},
  )
    .filter(([, tool]) => tool.type === 'custom')
    .map(([name]) => name)
  for (const name of customNames) {
    expect(button(`Edit ${name}`).closest('section')?.querySelector('h3')?.textContent).toBe(
      'Tools',
    )
    expect(button(`Remove ${name}`)).toBeDefined()
    expect(
      container.querySelector(`[role="radiogroup"][aria-label="${name} permission"]`),
    ).not.toBeNull()
  }
})

it.each(['yaml', 'json'])(
  'stages full definitions in %s, validates, and cancels without losing configuration edits',
  async (format) => {
    const source = format === 'json' ? JSON.stringify(parse(customFixture), null, 2) : customFixture
    const editor = await renderAdvancedEditor(source)
    act(() => {
      editor().form.setInstruction('Pending instruction.')
      button('Edit search_records').click()
    })
    const before = editor().yaml
    const definition =
      '{"type":"custom","description":"Updated example.","input_schema":{"type":"object","properties":{"nested":{"anyOf":[{"type":"string"},{"type":"object"}],"const":9007199254740993}}}}'
    await enter(
      'Definition',
      '{"type":"custom","description":"Example.","input_schema":{"type":"array"}}',
    )
    expect(button('Save tool').disabled).toBe(true)
    expect(document.querySelector('[role="dialog"] [role="alert"]')?.textContent).toContain(
      '/tools/search_records/input_schema/type',
    )
    expect(editor().yaml).toBe(before)
    clickLabel('Cancel')
    expect(editor().yaml).toBe(before)
    clickLabel('Edit search_records')
    await enter('Definition', definition)
    clickLabel('Save tool')
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(editor().saveBlocked).toBe(false)
    expect(parse(editor().yaml)).toHaveProperty(['tools', 'search_records'], parse(definition))
    expect(editor().yaml).toContain('9007199254740993')
  },
)

it.each(['yaml', 'json'])(
  'edits custom permissions and deletes only the chosen tool in %s',
  async (format) => {
    const source = format === 'json' ? JSON.stringify(parse(customFixture), null, 2) : customFixture
    const editor = await renderAdvancedEditor(source)
    clickLabel('Edit search_records')
    clickLabel('Save tool')
    expect(editor().yaml).toBe(source)
    const original = editor().form.advancedEntries.find(
      (entry) => entry.path[1] === 'search_records',
    )
    const permission = container.querySelector<HTMLButtonElement>(
      '[aria-label="search_records permission"] [aria-label="always_ask"]',
    )
    expect(permission?.disabled).toBe(false)
    act(() => permission?.click())
    expect(editor().saveBlocked).toBe(false)
    expect(parse(editor().yaml)).toHaveProperty(
      ['tools', 'search_records', 'permission', 'mode'],
      'always_ask',
    )
    const changed = editor().form.advancedEntries.find(
      (entry) => entry.path[1] === 'search_records',
    )
    for (const key of ['description', 'input_schema']) {
      const before = parseDocument(original?.source ?? '').getIn([key], true)
      const after = parseDocument(changed?.source ?? '').getIn([key], true)
      if (!isNode(before) || !isNode(after) || !before.range || !after.range)
        throw new Error('Missing definition')
      expect(changed?.source.slice(after.range[0], after.range[2])).toBe(
        original?.source.slice(before.range[0], before.range[2]),
      )
    }
    act(() => {
      editor().form.setAdvancedEntries(
        editor().form.advancedEntries.map((entry) =>
          entry.path[1] === 'search_records'
            ? { ...entry, source: 'type: custom\ninput_schema: [' }
            : entry,
        ),
      )
    })
    expect(editor().saveBlocked).toBe(true)
    expect(
      container.querySelector<HTMLButtonElement>('[aria-label="search_records permission"] button')
        ?.disabled,
    ).toBe(true)
    clickLabel('Remove search_records')
    expect(editor().saveBlocked).toBe(false)
    const expected = schemas.zAgentConfigDefinition.parse(parse(source))
    delete expected.tools?.search_records
    expect(parse(editor().yaml)).toEqual(expected)
  },
)

it.each([includedSource, 'instruction: [', JSON.stringify(parse(includedSource))])(
  'adopts custom definitions without changing the Source editor language while typing (%#)',
  async (source) => {
    const editor = await renderAdvancedEditor(source.startsWith('{') ? includedSource : source)
    const updated = source.startsWith('{') ? JSON.stringify(parse(customFixture)) : customFixture
    editYaml(editor, updated)
    expect(editor().editorFormat).toBe('yaml')
    act(() => {
      editor().switchMode('builder')
    })
    expect(editor().showBuilder).toBe(true)
    expect(editor().sourceFormat).toBe(source.startsWith('{') ? 'json' : 'yaml')
    expect(editor().editorFormat).toBe(editor().sourceFormat)
    expect(editor().yaml).toBe(updated)
    expect(container.querySelectorAll('button[aria-label^="Edit " ]')).toHaveLength(8)
  },
)

it.each([
  ['instruction: [', 'yaml', true, 'line 1'],
  [
    customFixture.replace('type: object', 'type: array'),
    'builder',
    false,
    '/tools/search_records/input_schema/type',
  ],
  [includedSource + 'tools: {web_search}\n', 'builder', false, '/tools/web_search'],
])(
  'retains invalid input and reports its location when adopting YAML: %s',
  async (yaml, mode, confirmDiscard, reason) => {
    const editor = await renderAdvancedEditor(includedSource)
    editYaml(editor, yaml)
    act(() => {
      editor().switchMode('builder')
    })
    expect(editor().mode).toMatchObject({ mode, confirmDiscard })
    expect(editor().yaml).toBe(yaml)
    expect(editor().saveBlocked).toBe(true)
    expect(diagnostics(editor())).toContain(reason)
  },
)

it('keeps valid anchors editable in YAML and explains why Builder is unavailable', async () => {
  const source = `${includedSource}tools: {web_search: &shared {}, web_fetch: *shared}\n`
  const editor = await renderAdvancedEditor(source)
  expect(editor().mode.mode).toBe('yaml')
  expect(editor().saveBlocked).toBe(false)
  expect(diagnostics(editor())).toContain('/tools/web_search: This configuration uses YAML anchors')
  act(() => {
    editor().switchMode('builder')
  })
  expect(editor().yaml).toBe(source)
})

it('retains subagent limits and tools when an opaque subagent remains', async () => {
  advancedProviders()
  const source = `${includedSource}subagents: {visible: {type: self}, preserved: {type: self, extra: true}}\nmax_subagents: 4\ntools: {spawn_agent: {permission: {mode: always_ask}}}\n`
  await renderAndFlush(<SubagentPreviewHarness source={source} />)
  clickLabel('Remove subagents')
  const yaml = container.querySelector('output')?.textContent ?? ''
  expect(parse(yaml)).toHaveProperty('max_subagents', 4)
  expect(parse(yaml)).toHaveProperty(['tools', 'spawn_agent', 'permission', 'mode'], 'always_ask')
  expect(parse(yaml)).toHaveProperty(['subagents', 'preserved', 'extra'], true)
  expect(container.querySelector('output')?.getAttribute('data-blocked')).toBe('false')
})

it('blocks a stale OAuth restore instead of overwriting a newer configuration', async () => {
  const baseline = createBasicConfigSession(customFixture).initialDraft
  if (!baseline) throw new Error('Missing fixture draft')
  savePendingMcpBuilderOAuth({
    returnPath: window.location.pathname,
    serverId: 'example-server',
    agentName: null,
    draft: { ...baseline, savedSource: customFixture, instruction: 'Pending edit.' },
  })
  window.history.replaceState(null, '', '?mcp_oauth=success&secret_id=example-secret&case=conflict')
  const editor = await renderAdvancedEditor(customFixture + 'extension: newer-value\n')
  expect(editor().saveBlocked).toBe(true)
  expect(diagnostics(editor())).toContain('changed during OAuth authorization')
  expect(editor().yaml).toContain('Pending edit.')
})

it('leaves schema validation of edited whole-document YAML to the API', async () => {
  const editor = await renderAdvancedEditor(includedSource)
  editYaml(editor, includedSource + 'version: future-version\n')
  expect(editor().mode.mode).toBe('yaml')
  expect(editor().saveBlocked).toBe(false)
  expect(diagnostics(editor())).toContain('/version')
})

function TemplateDraftHarness({ state }: { state: Ref<ReturnType<typeof useAgentDraft>> }) {
  const draft = useAgentDraft(undefined, undefined, undefined, undefined, {
    orgId: 'org-test',
    projectId: 'project-test',
  })
  useImperativeHandle(state, () => draft)
  return null
}

it.each(['yaml', 'oauth'])(
  'replaces the source document when a template is applied after %s restoration',
  async (mode) => {
    advancedProviders()
    const source = customFixture + 'extension: retained-until-template\n'
    if (mode === 'oauth') {
      const draft = createBasicConfigSession(source).initialDraft
      if (!draft) throw new Error('Missing fixture draft')
      savePendingMcpBuilderOAuth({
        returnPath: window.location.pathname,
        serverId: 'example-server',
        agentName: null,
        draft,
      })
      window.history.replaceState(
        null,
        '',
        '?mcp_oauth=success&secret_id=example-secret&case=template',
      )
    }
    const state = createRef<ReturnType<typeof useAgentDraft>>()
    await renderAndFlush(<TemplateDraftHarness state={state} />)
    const draft = () => {
      if (!state.current) throw new Error('Missing draft')
      return state.current
    }
    if (mode === 'yaml') {
      editYaml(draft, source)
      act(() => {
        draft().switchMode('builder')
      })
    }
    expect(draft().yaml).toContain('search_records')
    const template = agentTemplates[0]
    if (!template) throw new Error('Missing template')
    act(() => {
      draft().applyTemplate(template)
    })
    expect(draft().mode.mode).toBe('builder')
    expect(draft().form.advancedEntries).toEqual([])
    expect(draft().yaml).not.toContain('search_records')
    expect(draft().yaml).not.toContain('retained-until-template')
    expect(parse(draft().yaml)).toHaveProperty('instruction', template.instruction)
  },
)

async function openCustomToolDialog() {
  await act(async () => {
    button('Add tools').dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }),
    )
    await Promise.resolve()
  })
  const option = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
    (item) => item.textContent === 'Custom tool…',
  )
  expect(option).toBeDefined()
  act(() => {
    option?.click()
  })
}

it.each(['yaml', 'json'])(
  'adds a custom tool in %s from Tools plus and leaves cancelled drafts untouched',
  async (format) => {
    const source = format === 'json' ? JSON.stringify(parse(customFixture), null, 2) : customFixture
    const editor = await renderAdvancedEditor(source)
    await openCustomToolDialog()
    const template = field('Definition').value
    expect(parse(template)).toEqual({
      type: 'custom',
      description: '',
      input_schema: { type: 'object', properties: {} },
    })
    expect(template.startsWith('{')).toBe(format === 'json')
    await enter('Tool name', 'cancelled_tool')
    clickLabel('Cancel')
    expect(editor().yaml).toBe(source)
    expect(editor().dirty).toBe(false)
    await openCustomToolDialog()
    const definition = 'type: custom\ndescription: New example.\ninput_schema: {type: object}\n'
    await enter('Tool name', 'new_tool')
    await enter('Definition', definition)
    expect(button('Add custom tool').disabled).toBe(format === 'json')
    if (format === 'json') {
      expect(document.querySelector('[role="dialog"] [role="alert"]')?.textContent).toContain(
        'Enter valid JSON',
      )
      await enter('Definition', JSON.stringify(parse(definition)))
    }
    for (const name of [
      'search_records',
      'web_search',
      'mcp__reserved',
      'bad-name',
      'call_deferred_tool',
    ]) {
      await enter('Tool name', name)
      expect(button('Add custom tool').disabled).toBe(true)
    }
    await enter('Tool name', 'new_tool')
    clickLabel('Add custom tool')
    expect(parse(editor().yaml)).toHaveProperty(['tools', 'new_tool'], parse(definition))
    expect(button('Edit new_tool').closest('section')?.querySelector('h3')?.textContent).toBe(
      'Tools',
    )
    clickLabel('Remove new_tool')
    expect(editor().yaml).toBe(source)
    expect(editor().dirty).toBe(false)
  },
)

it('renames the only custom tool, rejects occupied names, and can restore its name', async () => {
  const source = `${includedSource}tools: {search_records: {type: custom, description: Search., input_schema: {type: object}}}\n`
  const editor = await renderAdvancedEditor(source)
  const original = schemas.zAgentConfigDefinition.parse(parse(editor().yaml)).tools?.search_records
  clickLabel('Edit search_records')
  await enter('Tool name', 'web_search')
  expect(button('Save tool').disabled).toBe(true)
  await enter('Tool name', 'renamed_search')
  clickLabel('Save tool')
  expect(parse(editor().yaml)).not.toHaveProperty(['tools', 'search_records'])
  expect(parse(editor().yaml)).toHaveProperty(['tools', 'renamed_search'], original)
  clickLabel('Edit renamed_search')
  await enter('Tool name', 'search_records')
  clickLabel('Save tool')
  expect(editor().yaml).toBe(source)
})
