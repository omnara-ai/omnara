/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, createRef, type Ref, useImperativeHandle, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it } from 'vitest'

import { configPath } from '@/components/agents/agentConfigCompatibility'
import customFixture from '@/components/agents/fixtures/inline-custom-tools.yaml?raw'
import {
  createBasicConfigSession,
  useAgentBuilderForm,
} from '@/components/agents/useAgentBuilderForm'
import { useAgentConfigMode } from '@/components/agents/useAgentConfigMode'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { enableReactActEnvironment } from '@/test/react-act'

const source = 'instruction: Help.\nmodel: {provider_config: example, name: primary}\n'
type State = ReturnType<typeof useAgentConfigMode> & {
  form: ReturnType<typeof useAgentBuilderForm>
}

function Harness({ state, source }: { state: Ref<State>; source: string }) {
  const [session, setSession] = useState(() => createBasicConfigSession(source))
  const form = useAgentBuilderForm(session, undefined, {
    orgId: 'org-test',
    projectId: 'project-test',
  })
  const mode = useAgentConfigMode({
    session,
    setSession,
    form,
    source,
    initialMode: session.initialDraft ? 'builder' : 'yaml',
    builderAvailable: session.initialDraft != null,
  })
  useImperativeHandle(state, () => ({ ...mode, form }))
  return null
}

async function withMode(source: string, run: (current: () => State) => void) {
  const restore = enableReactActEnvironment()
  const root = createRoot(document.createElement('div'))
  const state = createRef<State>()
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({
    fetch: fakeApi([
      {
        method: 'POST',
        path: '/api/v1/orgs/org-test/projects/project-test/agent-configs/tools',
        respond: () => jsonResponse({ tools: [] }),
      },
    ]).fetch,
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const current = () => {
    if (!state.current) throw new Error('Missing editor')
    return state.current
  }
  try {
    await act(async () => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={queryClient}>
            <Harness state={state} source={source} />
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    run(current)
  } finally {
    act(() => {
      root.unmount()
    })
    restore()
  }
}

function editSource(current: () => State, yaml: string) {
  act(() => {
    current().switchMode('yaml')
    current().dispatchMode({
      type: 'editor-yaml-changed',
      yaml,
      builderYaml: current().builderYaml,
    })
  })
}

it.each(['instruction', 'model'])(
  'retains the %s save gate when switching to source without edits',
  async (invalid) => {
    await withMode(source, (current) => {
      expect(current().saveBlocked).toBe(false)
      act(() => {
        const { form } = current()
        if (invalid === 'instruction') form.setInstruction('')
        if (invalid === 'model') form.reportModelUnavailable(true)
      })
      expect(current().saveBlocked).toBe(true)
      act(() => {
        current().switchMode('yaml')
      })
      expect(current().mode.mode).toBe('yaml')
      expect(current().saveBlocked).toBe(true)
      editSource(current, source + '# corrected source\n')
      expect(current().saveBlocked).toBe(false)
    })
  },
)

it('blocks saving or switching to source while an MCP name collision is unapplied', async () => {
  await withMode(
    source +
      'mcp: {preserved: {url: "https://example.test/mcp", extension: true}, managed: {url: "https://example.test/mcp"}}\n',
    (current) => {
      act(() => {
        current().form.setMcpServers(
          current().form.mcpServers.map((server) => ({ ...server, name: 'preserved' })),
        )
      })
      expect(current().form.unappliedChanges).toBe(true)
      expect(current().saveBlocked).toBe(true)
      act(() => {
        current().switchMode('yaml')
      })
      expect(current().mode.mode).toBe('builder')
      expect(current().form.mcpServers[0]?.name).toBe('preserved')
      act(() => {
        current().form.setMcpServers(
          current().form.mcpServers.map((server) => ({ ...server, name: 'managed' })),
        )
      })
      expect(current().saveBlocked).toBe(false)
      act(() => {
        current().switchMode('yaml')
      })
      expect(current().mode.mode).toBe('yaml')
    },
  )
})

it('leaves schema validation of edited whole-document source to the API', async () => {
  await withMode(source, (current) => {
    editSource(current, source + 'version: future-version\n')
    expect(current().saveBlocked).toBe(false)
    expect(current().compatibility.some((issue) => issue.path[0] === 'version')).toBe(true)
  })
})

it.each([
  ['instruction: [\n', 'yaml', true, 'line 2'],
  [
    customFixture.replace('type: object', 'type: array'),
    'builder',
    false,
    '/tools/search_records/input_schema/type',
  ],
  [source + 'tools: {web_search}\n', 'builder', false, '/tools/web_search'],
] as const)(
  'retains invalid input and reports its location when adopting YAML: %s',
  async (yaml, mode, confirmDiscard, reason) => {
    await withMode(source, (editor) => {
      editSource(editor, yaml)
      act(() => {
        editor().switchMode('builder')
      })
      expect(editor().mode).toMatchObject({ mode, confirmDiscard })
      expect(editor().yaml).toBe(yaml)
      expect(editor().saveBlocked).toBe(true)
      expect(
        editor()
          .compatibility.map((issue) => `${configPath(issue.path)}: ${issue.message}`)
          .join('; '),
      ).toContain(reason)
    })
  },
)
