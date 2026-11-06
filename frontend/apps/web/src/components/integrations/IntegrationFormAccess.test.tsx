/** @vitest-environment happy-dom */

import { schemas } from '@omnara/sdk'
import type { QueryClient } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { fakeApi } from '@/test/fake-api'
import { fakeId, integration as integrationFixture } from '@/test/fixtures'
import { renderIntegration } from '@/test/integration-render'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, field, waitForUI } from '@/test/secret-editor'

import { IntegrationForm } from './IntegrationForm'

let root: Root, container: HTMLDivElement, cache: QueryClient, restore: () => void
beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  cache.clear()
  container.remove()
  restore()
})

it('saves, discards, and clears external Slack access independently of mention launches', async () => {
  const orgId = fakeId('org'),
    projectId = fakeId('proj')
  const path = `/api/v1/orgs/${orgId}/projects/${projectId}`
  const integration = integrationFixture({ settings: {} })
  const updatePath = path + '/integrations/' + integration.id
  let revision = 0
  const onSaved = vi.fn()
  const api = fakeApi([
    {
      method: 'GET',
      path: path + '/agent-profiles',
      respond: () => Response.json({ data: [], next_cursor: null }),
    },
    {
      method: 'PUT',
      path: updatePath,
      respond: ({ body }) =>
        Response.json({
          ...integration,
          ...schemas.zUpdateIntegrationRequest.parse(body),
          updated_at: `2026-09-2${++revision}T00:00:00Z`,
        }),
    },
  ])
  cache = renderIntegration(
    root,
    api,
    <IntegrationForm
      orgId={orgId}
      projectId={projectId}
      integrationKind="slack_thread"
      integration={integration}
      onSaved={onSaved}
    />,
  ).cache
  const label = 'Allow external participants in shared channels'
  expect(field(label)).toHaveProperty('checked', false)
  act(() => {
    field(label).click()
  })
  expect(button('Save changes').disabled).toBe(false)
  act(() => {
    button('Discard changes').click()
  })
  expect(field(label)).toHaveProperty('checked', false)
  act(() => {
    field(label).click()
  })
  act(() => {
    button('Save changes').click()
  })
  await waitForUI(() => {
    expect(onSaved).toHaveBeenCalledTimes(1)
  })
  expect(api.requestsTo('PUT', updatePath)[0]?.body).toEqual({
    settings: { allow_external_participants: true },
  })
  act(() => {
    field(label).click()
  })
  act(() => {
    button('Save changes').click()
  })
  await waitForUI(() => {
    expect(onSaved).toHaveBeenCalledTimes(2)
  })
  expect(api.requestsTo('PUT', updatePath)[1]?.body).toEqual({ settings: {} })
})
