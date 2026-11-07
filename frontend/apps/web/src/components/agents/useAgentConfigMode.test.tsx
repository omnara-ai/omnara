/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, createRef, type Ref, useImperativeHandle, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it } from 'vitest'

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

function Harness({ state }: { state: Ref<State> }) {
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
    initialMode: 'builder',
    builderAvailable: true,
  })
  useImperativeHandle(state, () => ({ ...mode, form }))
  return null
}

it.each(['instruction', 'model'])(
  'retains the %s save gate when switching to source without edits',
  async (invalid) => {
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
              <Harness state={state} />
            </QueryClientProvider>
          </OmnaraClientProvider>,
        )
        await new Promise((resolve) => setTimeout(resolve, 0))
      })
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
      act(() => {
        current().dispatchMode({
          type: 'editor-yaml-changed',
          yaml: source + '# corrected source\n',
          builderYaml: current().builderYaml,
        })
      })
      expect(current().saveBlocked).toBe(false)
    } finally {
      act(() => {
        root.unmount()
      })
      restore()
    }
  },
)
