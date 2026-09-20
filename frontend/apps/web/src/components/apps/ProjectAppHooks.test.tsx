/** @vitest-environment happy-dom */
import {
  OmnaraClientProvider,
  useAppDefinitions,
  useConfigureProjectApp,
  useDisconnectProjectApp,
  useProjectApp,
} from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { type FakeApi, fakeApi } from '@/test/fake-api'
import { appDefinition, fakeId, projectApp } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, waitForUI } from '@/test/secret-editor'

const orgID = fakeId('org'),
  projectID = fakeId('proj')
const path = `/api/v1/orgs/${orgID}/projects/${projectID}`
const app = projectApp()
let root: Root, container: HTMLDivElement, cache: QueryClient, restore: () => void
beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
  cache = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  cache.clear()
  container.remove()
  restore()
})
function render(api: FakeApi, node: ReactNode) {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={cache}>{node}</QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })
}
function Catalog() {
  const query = useAppDefinitions(orgID, projectID)
  return <div>{query.data?.data.map((definition) => definition.id).join(',')}</div>
}
it('loads the registry through the project-scoped catalog hook', async () => {
  const api = fakeApi([
    {
      method: 'GET',
      path: path + '/app-definitions',
      respond: () => Response.json({ data: [appDefinition()] }),
    },
  ])
  render(api, <Catalog />)
  await waitForUI(() => {
    expect(container.textContent).toContain('omnara.slack')
  })
  expect(api.requestsTo('GET', path + '/app-definitions')).toHaveLength(1)
})
function Setup() {
  const detail = useProjectApp(orgID, projectID, app.id)
  const configure = useConfigureProjectApp(orgID, projectID)
  const disconnect = useDisconnectProjectApp(orgID, projectID)
  return (
    <div>
      <output>
        {detail.data?.state}/{detail.data?.setup_revision}
      </output>
      <button
        onClick={() => {
          configure.mutate({
            appID: app.id,
            expected_setup_revision: detail.data?.setup_revision ?? 1,
            provider_tenant_id: 'T123',
            provider_account_ref: 'A123',
            credential_secret_id: fakeId('sec'),
          })
        }}
      >
        Configure
      </button>
      <button
        onClick={() => {
          disconnect.mutate(app.id)
        }}
      >
        Disconnect
      </button>
    </div>
  )
}
it('uses app setup and disconnect responses to refresh the same detail cache', async () => {
  const api = fakeApi([
    { method: 'GET', path: path + '/apps/' + app.id, respond: () => Response.json(app) },
    {
      method: 'POST',
      path: path + '/apps/' + app.id + '/setup',
      respond: () => Response.json({ ...app, state: 'active', setup_revision: 2 }),
    },
    {
      method: 'POST',
      path: path + '/apps/' + app.id + '/disconnect',
      respond: () => Response.json({ ...app, state: 'disconnected', setup_revision: 3 }),
    },
  ])
  render(api, <Setup />)
  await waitForUI(() => {
    expect(container.querySelector('output')?.textContent).toBe('disconnected/1')
  })
  act(() => {
    button('Configure').click()
  })
  await waitForUI(() => {
    expect(container.querySelector('output')?.textContent).toBe('active/2')
  })
  expect(api.requestsTo('POST', path + '/apps/' + app.id + '/setup')[0]?.body).toEqual({
    expected_setup_revision: 1,
    provider_tenant_id: 'T123',
    provider_account_ref: 'A123',
    credential_secret_id: fakeId('sec'),
  })
  act(() => {
    button('Disconnect').click()
  })
  await waitForUI(() => {
    expect(container.querySelector('output')?.textContent).toBe('disconnected/3')
  })
  expect(api.requestsTo('GET', path + '/apps/' + app.id)).toHaveLength(1)
})
