/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import {
  type AgentProfileSummary,
  createOmnaraClient,
  type CronTrigger,
  type IntegrationCronTriggerTarget,
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
import { afterEach, beforeEach, expect, vi } from 'vitest'

import { type FakeApi } from '@/test/fake-api'
import { agentConfigModel, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

export const orgId = fakeId('org'),
  projectId = fakeId('proj')
export const path = `/api/v1/orgs/${orgId}/projects/${projectId}`
export const cronPath = path + '/cron-triggers'
export const now = '2026-09-20T09:00:00Z'
export const profile: AgentProfileSummary = {
  id: fakeId('aprf'),
  org_id: orgId,
  project_id: projectId,
  name: 'Daily reporter',
  current_generation: 1,
  current_config_id: fakeId('acfg'),
  current_config: {
    id: fakeId('acfg'),
    org_id: orgId,
    project_id: projectId,
    model: agentConfigModel(),
    effective_definition_hash: 'hash',
    created_at: now,
  },
  created_at: now,
  updated_at: now,
}
export const integrationTarget: IntegrationCronTriggerTarget = {
  type: 'integration',
  integration_id: fakeId('itg'),
  settings: {
    agent_profile_id: profile.id,
    channel_id: 'C123',
    opening_message_template: '{{.trigger.name}} — {{.trigger.local_date}}',
    message_template: 'Summarize the daily progress.',
  },
}
export function trigger(overrides: Partial<CronTrigger> = {}): CronTrigger {
  return {
    id: fakeId('cron'),
    org_id: orgId,
    project_id: projectId,
    name: 'daily-report',
    target: integrationTarget,
    cron: '0 9 * * 1-5',
    timezone: 'UTC',
    enabled: true,
    last_fired_at: null,
    next_fire_at: now,
    failure_report: null,
    last_run: null,
    created_at: now,
    updated_at: now,
    ...overrides,
  }
}
export const profileRoutes = [
  {
    method: 'GET',
    path: path + '/agent-profiles',
    respond: () => Response.json({ data: [profile], next_cursor: null }),
  },
  {
    method: 'GET',
    path: path + '/agent-profiles/' + profile.id,
    respond: () =>
      Response.json({
        ...profile,
        current_config: {
          ...profile.current_config,
          compiled_definition: {
            instruction: 'Report daily progress.',
            model: { configured_model_id: fakeId('mdl') },
          },
        },
      }),
  },
]

let root: Root, restore: () => void
export let container: HTMLDivElement, cache: QueryClient
beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
  cache = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: 30_000 }, mutations: { retry: false } },
  })
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  cache.clear()
  container.remove()
  restore()
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})
export function render(api: FakeApi, node: ReactNode) {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  const router = createRouter({ routeTree: createRootRoute(), history: createMemoryHistory() })
  const rerender = (content: ReactNode) => {
    act(() => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>
            <RouterContextProvider router={router}>{content}</RouterContextProvider>
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
    })
  }
  rerender(node)
  return rerender
}
export async function submit() {
  await act(async () => {
    const form = document.querySelector('form')
    if (!form) throw new Error('Missing cron form')
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await Promise.resolve()
  })
}
export async function chooseProfile(selected = profile) {
  act(() => {
    const picker = document.getElementById('integration-profiles')
    if (!(picker instanceof HTMLButtonElement)) throw new Error('Missing profile picker')
    picker.click()
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="option"]')).not.toBeNull()
  })
  act(() => {
    const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(
      (item) => item.textContent === selected.name,
    )
    if (!option) throw new Error('Missing profile option')
    option.click()
  })
}
