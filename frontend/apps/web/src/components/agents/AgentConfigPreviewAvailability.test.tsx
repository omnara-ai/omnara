/** @vitest-environment happy-dom */

import { OmnaraClientProvider, useAgentConfigTools } from '@omnara/react'
import { createOmnaraClient, schemas } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act, createContext, type ReactNode, StrictMode, useContext } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import { parse } from 'yaml'
import { z } from 'zod'

import { AgentConfigAdvancedFields } from '@/components/agents/AgentConfigAdvancedFields'
import { configPath } from '@/components/agents/agentConfigCompatibility'
import { AgentConfigEditorFields } from '@/components/agents/AgentConfigEditor'
import { agentTemplates } from '@/components/agents/agentTemplates'
import customFixture from '@/components/agents/fixtures/inline-custom-tools.yaml?raw'
import { savePendingMcpBuilderOAuth } from '@/components/agents/pendingMcpBuilderOAuth'
import {
  createBasicConfigSession,
  useAgentBuilderForm,
} from '@/components/agents/useAgentBuilderForm'
import { useAgentConfigEditor } from '@/components/agents/useAgentConfigEditor'
import { useAgentDraft } from '@/components/agents/useAgentDraft'
import { ActiveOrgContext } from '@/lib/active-org-context'
import { fakeApi, type FakeRoute, jsonResponse } from '@/test/fake-api'
import { currentUserOrg } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'

const includedSource = 'instruction: Help.\nmodel: {provider_config: openai, name: primary}\n'
let container: HTMLDivElement
let root: Root
let restoreActEnvironment: () => void
let Providers: ReturnType<typeof testProviders>

beforeAll(() => {
  restoreActEnvironment = enableReactActEnvironment()
})
afterAll(() => {
  restoreActEnvironment()
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
  const button = [...container.querySelectorAll('button')].find(
    (item) => item.textContent === label,
  )
  if (!button) throw new Error(`Missing ${label}`)
  act(() => {
    button.click()
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
  Providers = testProviders([
    {
      method: 'POST',
      path: '/api/v1/orgs/org-test/projects/project-test/agent-configs/tools',
      respond: () => jsonResponse({ tools: [] }),
    },
  ])
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
  source = customFixture,
  adoptedYaml = customFixture,
  fullEditor = false,
}: {
  source?: string
  adoptedYaml?: string
  fullEditor?: boolean
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
  return (
    <>
      {fullEditor ? (
        <AgentConfigEditorFields
          editor={editor}
          orgId="org-test"
          projectId="project-test"
          yamlFieldId="test-yaml"
          yamlFieldClassName=""
        />
      ) : (
        <AgentConfigAdvancedFields form={editor.form} />
      )}
      <button
        type="button"
        onClick={() => {
          editor.switchMode('yaml')
        }}
      >
        Show YAML
      </button>
      <button
        type="button"
        onClick={() => {
          editor.form.setInstruction('Pending instruction.')
        }}
      >
        Change instruction
      </button>
      <button
        type="button"
        onClick={() => {
          editor.switchMode('yaml')
          editor.dispatchMode({
            type: 'editor-yaml-changed',
            yaml: adoptedYaml,
            builderYaml: editor.builderYaml,
          })
        }}
      >
        Edit YAML
      </button>
      <button
        type="button"
        onClick={() => {
          editor.switchMode('builder')
        }}
      >
        Adopt Builder
      </button>
      <output
        data-mode={editor.mode.mode}
        data-blocked={editor.saveBlocked}
        data-dirty={editor.dirty}
        data-confirm-discard={editor.mode.confirmDiscard}
      >
        {editor.yaml}
      </output>
      <p data-diagnostics>
        {editor.compatibility
          .map((issue) => `${configPath(issue.path)}: ${issue.message}`)
          .join('; ')}
      </p>
    </>
  )
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

it('shows normal Builder controls and every custom tool without a whole-document warning', async () => {
  advancedProviders()
  await renderAndFlush(<AdvancedEditorHarness fullEditor />)
  expect(container.querySelector('output')?.getAttribute('data-mode')).toBe('builder')
  expect(container.querySelector('#agent-config-basic-instruction')).not.toBeNull()
  expect(container.textContent).toContain('8 custom tool definitions are preserved')
  expect(container.textContent).not.toContain('editable as YAML only')
  for (const name of [
    'search_records',
    'publish_report',
    'get_record',
    'list_records',
    'archive_record',
    'list_reports',
    'get_report',
    'archive_report',
  ]) {
    expect(
      [...container.querySelectorAll('summary')].some((summary) =>
        summary.textContent.includes(name),
      ),
    ).toBe(true)
  }
})

it('validates the embedded raw editor, blocks invalid saves, and saves intentional schema edits', async () => {
  advancedProviders()
  await renderAndFlush(<AdvancedEditorHarness />)
  const textarea = container.querySelector<HTMLTextAreaElement>(
    '[id="advanced-/tools/search_records"]',
  )
  if (!textarea) throw new Error('Missing advanced editor')
  const change = (value: string) => {
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set?.call(
        textarea,
        value,
      )
      textarea.dispatchEvent(new Event('input', { bubbles: true }))
    })
  }
  change('type: custom\ndescription: Example\ninput_schema: {type: array}')
  expect(container.querySelector('output')?.getAttribute('data-blocked')).toBe('true')
  expect(container.querySelector('output')?.getAttribute('data-dirty')).toBe('true')
  expect(container.querySelector('[role="alert"]')?.textContent).toContain(
    '/tools/search_records/input_schema/type',
  )
  expect(container.querySelector('output')?.textContent).toBe(customFixture)
  const raw =
    'type: custom\ndescription: Updated description.\ninput_schema: {type: object, properties: {nested: {type: array, items: {anyOf: [{type: string}, {type: object}]}}}}'
  change(raw)
  expect(container.querySelector('output')?.getAttribute('data-blocked')).toBe('false')
  expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
    ['tools', 'search_records'],
    parse(raw),
  )
})

it('adopts YAML after adding valid inline custom tools', async () => {
  advancedProviders()
  await renderAndFlush(<AdvancedEditorHarness source={includedSource} />)
  clickLabel('Edit YAML')
  clickLabel('Adopt Builder')
  expect(container.querySelector('output')?.getAttribute('data-mode')).toBe('builder')
  expect(container.querySelector('output')?.textContent).toBe(customFixture)
  expect(container.textContent).toContain('8 custom tool definitions are preserved')
})

it.each([
  ['instruction: [', 'YAML'],
  [customFixture.replace('type: object', 'type: array'), '/tools/search_records/input_schema/type'],
])(
  'retains invalid YAML on failed Builder adoption with a useful reason',
  async (adoptedYaml, reason) => {
    advancedProviders()
    await renderAndFlush(
      <AdvancedEditorHarness source={includedSource} adoptedYaml={adoptedYaml} />,
    )
    clickLabel('Edit YAML')
    clickLabel('Adopt Builder')
    expect(container.querySelector('output')?.getAttribute('data-mode')).toBe(
      reason === 'YAML' ? 'yaml' : 'builder',
    )
    expect(container.querySelector('output')?.getAttribute('data-confirm-discard')).toBe(
      reason === 'YAML' ? 'true' : 'false',
    )
    expect(container.querySelector('output')?.textContent).toBe(adoptedYaml)
    expect(container.querySelector('output')?.getAttribute('data-blocked')).toBe('true')
    if (reason === 'YAML')
      expect(container.querySelector('[data-diagnostics]')?.textContent).toContain('line 1')
    else expect(container.querySelector('[data-diagnostics]')?.textContent).toContain(reason)
  },
)

it('recovers from initially fatal YAML after correcting it', async () => {
  advancedProviders()
  await renderAndFlush(<AdvancedEditorHarness source="instruction: [" />)
  expect(container.querySelector('output')?.getAttribute('data-mode')).toBe('yaml')
  clickLabel('Edit YAML')
  clickLabel('Adopt Builder')
  expect(container.querySelector('output')?.getAttribute('data-mode')).toBe('builder')
  expect(container.querySelector('output')?.textContent).toBe(customFixture)
})

it('keeps valid anchors editable in YAML and explains why Builder is unavailable', async () => {
  advancedProviders()
  const source = `${includedSource}tools: {web_search: &shared {}, web_fetch: *shared}\n`
  await renderAndFlush(<AdvancedEditorHarness source={source} />)
  expect(container.querySelector('output')?.getAttribute('data-mode')).toBe('yaml')
  expect(container.querySelector('output')?.getAttribute('data-blocked')).toBe('false')
  expect(container.querySelector('[data-diagnostics]')?.textContent).toContain(
    '/tools/web_search: This configuration uses YAML anchors',
  )
  clickLabel('Adopt Builder')
  expect(container.querySelector('output')?.textContent).toBe(source)
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
  advancedProviders()
  const baseline = createBasicConfigSession(customFixture).initialDraft
  if (!baseline) throw new Error('Missing fixture draft')
  const previousUrl = window.location.href
  savePendingMcpBuilderOAuth({
    returnPath: window.location.pathname,
    serverId: 'example-server',
    agentName: null,
    draft: { ...baseline, savedSource: customFixture, instruction: 'Pending edit.' },
  })
  window.history.replaceState(null, '', '?mcp_oauth=success&secret_id=example-secret&case=conflict')
  try {
    await renderAndFlush(
      <AdvancedEditorHarness source={customFixture + 'extension: newer-value\n'} />,
    )
    expect(container.querySelector('output')?.getAttribute('data-blocked')).toBe('true')
    expect(container.querySelector('[data-diagnostics]')?.textContent).toContain(
      'changed during OAuth authorization',
    )
    expect(container.querySelector('output')?.textContent).toContain('Pending edit.')
  } finally {
    window.history.replaceState(null, '', previousUrl)
    window.sessionStorage.clear()
  }
})

it('keeps the editor mounted when YAML contains a value-less tool entry', async () => {
  advancedProviders()
  const source = includedSource + 'tools: {web_search}\n'
  await renderAndFlush(<AdvancedEditorHarness source={includedSource} adoptedYaml={source} />)
  clickLabel('Edit YAML')
  expect(container.querySelector('output')?.textContent).toBe(source)
  expect(container.querySelector('[data-diagnostics]')?.textContent).toContain('/tools/web_search')
})

it('leaves schema validation of edited whole-document YAML to the API', async () => {
  advancedProviders()
  const source = includedSource + 'version: future-version\n'
  await renderAndFlush(<AdvancedEditorHarness source={includedSource} adoptedYaml={source} />)
  clickLabel('Edit YAML')
  expect(container.querySelector('output')?.getAttribute('data-mode')).toBe('yaml')
  expect(container.querySelector('output')?.getAttribute('data-blocked')).toBe('false')
  expect(container.querySelector('[data-diagnostics]')?.textContent).toContain('/version')
})

it('retains unfinished raw edits until they are fixed or explicitly discarded', async () => {
  advancedProviders()
  await renderAndFlush(<AdvancedEditorHarness />)
  clickLabel('Change instruction')
  const textarea = container.querySelector<HTMLTextAreaElement>(
    '[id="advanced-/tools/search_records"]',
  )
  if (!textarea) throw new Error('Missing custom editor')
  const invalid = 'type: custom\ndescription: Pending\ninput_schema: ['
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set?.call(
      textarea,
      invalid,
    )
    textarea.dispatchEvent(new Event('input', { bubbles: true }))
  })
  clickLabel('Show YAML')
  expect(container.querySelector('output')?.getAttribute('data-mode')).toBe('builder')
  expect(container.querySelector('output')?.getAttribute('data-blocked')).toBe('true')
  expect(container.querySelector('output')?.getAttribute('data-dirty')).toBe('true')
  expect(textarea.value).toBe(invalid)
  expect(container.querySelector('[data-diagnostics]')?.textContent).toContain('Fix or discard')
  clickLabel('Discard edits to search_records')
  clickLabel('Show YAML')
  expect(container.querySelector('output')?.getAttribute('data-mode')).toBe('yaml')
  expect(container.querySelector('output')?.getAttribute('data-blocked')).toBe('false')
  expect(parse(container.querySelector('output')?.textContent ?? '')).toHaveProperty(
    'instruction',
    'Pending instruction.',
  )
})

function TemplateDraftHarness() {
  const draft = useAgentDraft(undefined, undefined, undefined, undefined, {
    orgId: 'org-test',
    projectId: 'project-test',
  })
  return (
    <>
      <button
        type="button"
        onClick={() => {
          draft.switchMode('yaml')
          draft.dispatchMode({
            type: 'editor-yaml-changed',
            yaml: customFixture + 'extension: retained-until-template\n',
            builderYaml: draft.builderYaml,
          })
        }}
      >
        Load custom draft
      </button>
      <button
        type="button"
        onClick={() => {
          draft.switchMode('builder')
        }}
      >
        Adopt custom draft
      </button>
      <button
        type="button"
        onClick={() => {
          const template = agentTemplates[0]
          if (!template) throw new Error('Missing template')
          draft.applyTemplate(template)
        }}
      >
        Apply template
      </button>
      <output data-mode={draft.mode.mode} data-custom-count={draft.form.advancedEntries.length}>
        {draft.yaml}
      </output>
    </>
  )
}

it.each(['yaml', 'oauth'])(
  'replaces the source document when a template is applied after %s restoration',
  async (mode) => {
    advancedProviders()
    const previousUrl = window.location.href
    if (mode === 'oauth') {
      const draft = createBasicConfigSession(
        customFixture + 'extension: retained-until-template\n',
      ).initialDraft
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
    try {
      await renderAndFlush(<TemplateDraftHarness />)
      if (mode === 'yaml') {
        clickLabel('Load custom draft')
        clickLabel('Adopt custom draft')
      }
      expect(container.querySelector('output')?.textContent).toContain('search_records')
      clickLabel('Apply template')
      const yaml = container.querySelector('output')?.textContent ?? ''
      expect(container.querySelector('output')?.getAttribute('data-mode')).toBe('builder')
      expect(container.querySelector('output')?.getAttribute('data-custom-count')).toBe('0')
      expect(yaml).not.toContain('search_records')
      expect(yaml).not.toContain('retained-until-template')
      expect(parse(yaml)).toHaveProperty('instruction', agentTemplates[0]?.instruction)
    } finally {
      window.history.replaceState(null, '', previousUrl)
      window.sessionStorage.clear()
    }
  },
)
