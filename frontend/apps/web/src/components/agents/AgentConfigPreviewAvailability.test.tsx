/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, schemas } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode } from 'react'
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
  return function Providers({ children }: { children: ReactNode }) {
    return (
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
      </OmnaraClientProvider>
    )
  }
}

async function renderAndFlush(node: ReactNode) {
  await act(async () => {
    root.render(<Providers>{node}</Providers>)
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
