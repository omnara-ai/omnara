/** @vitest-environment happy-dom */

import { OmnaraClientProvider, useAgentConfigTools } from '@omnara/react'
import { createOmnaraClient, schemas } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterContextProvider,
} from '@tanstack/react-router'
import { act, type ReactNode, StrictMode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import { parse } from 'yaml'

import {
  createBasicConfigSession,
  useAgentBuilderForm,
} from '@/components/agents/useAgentBuilderForm'
import { useAgentConfigEditor } from '@/components/agents/useAgentConfigEditor'
import { useAgentDraft } from '@/components/agents/useAgentDraft'
import { fakeApi, type FakeRoute, jsonResponse } from '@/test/fake-api'
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

function testProviders(routes: FakeRoute[]) {
  const api = fakeApi(routes)
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
          <RouterContextProvider router={router}>{children}</RouterContextProvider>
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
              modelOverride: { name: 'another-model' },
            })),
          )
        }}
      >
        Edit subagent details
      </button>
      <output data-pending={form.toolsPending}>{form.yaml}</output>
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
  expect(JSON.parse(request.source)).toHaveProperty('subagents.worker', {
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

function ToolPreviewHarness({ source }: { source: string }) {
  const query = useAgentConfigTools('org-test', 'project-test', {
    source_format: 'yaml',
    source,
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
        const { source } = schemas.zResolveAgentConfigToolsRequest.parse(body)
        requests.push(source)
        if (source === original) return previewResponse(['read_file'])
        if (source === updated) return previewResponse(['read_file', 'run_command'])
        if (source === failed && fail) {
          return jsonResponse({ code: 'internal_error', message: 'Unavailable' }, 500)
        }
        return pending.then((response) => response.clone())
      },
    },
  ])
  await renderAndFlush(<ToolPreviewHarness source={original} />)
  await vi.waitFor(() => {
    expect(container.textContent).toBe('read_file')
  })
  await renderAndFlush(<ToolPreviewHarness source={failed} />)
  await vi.waitFor(() => {
    expect(container.querySelector('output')?.getAttribute('data-status')).toBe('error')
  })
  expect(container.textContent).toBe('read_file')
  await renderAndFlush(<ToolPreviewHarness source={updated} />)
  await vi.waitFor(() => {
    expect(container.textContent).toBe('read_file,run_command')
  })
  fail = false
  await renderAndFlush(<ToolPreviewHarness source={failed} />)
  expect(container.querySelector('output')?.getAttribute('data-fetching')).toBe('true')
  expect(container.textContent).toBe('read_file,run_command')
  await renderAndFlush(<ToolPreviewHarness source={original} />)
  expect(container.textContent).toBe('read_file')
  expect(requests.filter((source) => source === original)).toHaveLength(1)
  await renderAndFlush(<ToolPreviewHarness source={next} />)
  expect(container.querySelector('output')?.getAttribute('data-fetching')).toBe('true')
  expect(container.textContent).toBe('read_file')
  await act(async () => {
    release(previewResponse([]))
    await pending
  })
  await vi.waitFor(() => {
    expect(container.querySelector('output')?.getAttribute('data-fetching')).toBe('false')
    expect(container.textContent).toBe('')
  })
})
