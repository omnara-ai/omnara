/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
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

import type { BasicSubagent } from '@/components/agents/agentConfigSubagents'
import { AgentConfigSubagentsField } from '@/components/agents/AgentConfigSubagentsField'
import { ActiveOrgContext } from '@/lib/active-org-context'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { currentUserOrg, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'

const TestContentContext = createContext<ReactNode>(null)
function TestContent() {
  return useContext(TestContentContext)
}
const testRouter = createRouter({
  routeTree: createRootRoute({ component: TestContent }),
  history: createMemoryHistory(),
})

const activeOrg = currentUserOrg({ id: fakeId('org') })
const clone: BasicSubagent = {
  id: 'clone',
  key: 'researcher',
  type: 'self',
  profileName: '',
  description: '',
  instructionAppend: '',
  maxInstances: '',
  archiveAfterIdleMinutes: '',
  modelOverride: {
    provider_config: 'omnara-openrouter',
    name: 'openai/gpt-5.6-sol',
    reasoning: { effort: 'low' },
    context_window_tokens: 64000,
  },
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

async function renderField(onSubagentsChange: (subagents: BasicSubagent[]) => void) {
  const client = createOmnaraClient({
    baseUrl: 'https://omnara.test/api/v1',
    fetch: fakeApi([
      {
        method: 'GET',
        path: `/api/v1/orgs/${activeOrg.id}/projects`,
        respond: () => jsonResponse({ data: [], next_cursor: null }),
      },
    ]).fetch,
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const field = (
    <AgentConfigSubagentsField
      orgId={activeOrg.id}
      projectId={fakeId('proj')}
      subagents={[clone]}
      parentReasoningEffort=""
      maxSubagents=""
      maxDepth=""
      onSubagentsChange={onSubagentsChange}
      onMaxSubagentsChange={() => undefined}
      onMaxDepthChange={() => undefined}
    />
  )
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <ActiveOrgContext.Provider
            value={{ orgs: [activeOrg], activeOrg, setActiveOrgId: () => undefined }}
          >
            <TestContentContext value={field}>
              <RouterProvider router={testRouter} />
            </TestContentContext>
          </ActiveOrgContext.Provider>
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

function clickTab(label: string) {
  const tab = [...container.querySelectorAll('button')].find(
    (candidate) => candidate.textContent === label,
  )
  if (!tab) throw new Error(`Missing tab: ${label}`)
  act(() => {
    tab.click()
  })
}

it('keeps the model override when the selected type is clicked again', async () => {
  const onSubagentsChange = vi.fn()
  await renderField(onSubagentsChange)

  clickTab('Clone')

  expect(onSubagentsChange).toHaveBeenCalled()
  for (const call of onSubagentsChange.mock.calls) {
    expect(call).toEqual([[clone]])
  }
})

it('keeps the model override when the type changes', async () => {
  const onSubagentsChange = vi.fn()
  await renderField(onSubagentsChange)

  clickTab('Agent profile')

  expect(onSubagentsChange).toHaveBeenLastCalledWith([{ ...clone, type: 'profile' }])
})
