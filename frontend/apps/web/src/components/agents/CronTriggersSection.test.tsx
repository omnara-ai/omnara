/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type CronTrigger } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'

import { CronTriggersList } from '@/components/agents/CronTriggersSection'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { translateTextNodes } from '@/test/translate-text-nodes'

const orgId = fakeId('org')
const projectId = fakeId('proj')
const timestamp = '2026-01-01T00:00:00Z'
const triggersPath = `/api/v1/orgs/${orgId}/projects/${projectId}/cron-triggers`

let container: HTMLDivElement
let root: Root
let restoreActEnvironment: () => void

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

it('pauses a schedule after a browser translator rewrites its next run', async () => {
  let trigger: CronTrigger = {
    id: fakeId('cron'),
    org_id: orgId,
    project_id: projectId,
    name: 'nightly-report',
    target: { type: 'profile', agent_profile_id: fakeId('aprf') },
    cron: '0 0 1 1 *',
    timezone: 'UTC',
    message_template: 'Write the report.',
    enabled: true,
    last_fired_at: null,
    next_fire_at: '2026-12-31T00:00:00Z',
    failure_report: null,
    created_at: timestamp,
    updated_at: timestamp,
  }
  const api = fakeApi([
    {
      method: 'GET',
      path: triggersPath,
      respond: () => jsonResponse({ data: [trigger], next_cursor: null }),
    },
    {
      method: 'PATCH',
      path: `${triggersPath}/${trigger.id}`,
      respond: () => {
        trigger = { ...trigger, enabled: false, next_fire_at: null }
        return jsonResponse(trigger)
      },
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <CronTriggersList
            orgId={orgId}
            projectId={projectId}
            canManage
            filters={{}}
            emptyMessage="No schedules"
          />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })
  await vi.waitFor(() => {
    expect(container.textContent).toContain('next')
  })
  translateTextNodes(container)

  act(() => {
    container
      .querySelector<HTMLButtonElement>('[aria-label="Disable schedule nightly-report"]')
      ?.click()
  })
  await vi.waitFor(() => {
    expect(container.querySelector('[aria-label="Enable schedule nightly-report"]')).not.toBeNull()
  })
  expect(container.textContent).not.toContain('next')
})
