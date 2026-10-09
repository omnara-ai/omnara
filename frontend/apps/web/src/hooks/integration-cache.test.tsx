/** @vitest-environment happy-dom */

import { OmnaraClientProvider, useDeleteIntegration } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import {
  getAgentConfigQueryKey,
  getIntegrationQueryKey,
  listAgentsQueryKey,
  listConfiguredModelsQueryKey,
  listCronTriggersQueryKey,
  listIntegrationsQueryKey,
  listOrgAgentsQueryKey,
  listOrgIntegrationsQueryKey,
} from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it } from 'vitest'

import { fakeApi } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, waitForUI } from '@/test/secret-editor'

const orgID = fakeId('org')
const projectID = fakeId('proj')
const integrationID = fakeId('itg')

function DeleteIntegration() {
  const remove = useDeleteIntegration(orgID, projectID)
  return (
    <>
      <button
        onClick={() => {
          remove.mutate(integrationID)
        }}
      >
        Delete integration
      </button>
      {remove.isSuccess && <p>Deleted</p>}
    </>
  )
}

it('invalidates organization and project agent lists when deleting a target, within the affected scope', async () => {
  const restore = enableReactActEnvironment()
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  const cache = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  const api = fakeApi([
    {
      method: 'DELETE',
      path: `/api/v1/orgs/${orgID}/projects/${projectID}/integrations/${integrationID}`,
      respond: () => new Response(null, { status: 204 }),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  const scope = { path: { orgID, projectID }, client }
  const changed = [
    listIntegrationsQueryKey(scope),
    listOrgIntegrationsQueryKey({ path: { orgID }, client }),
    listOrgIntegrationsQueryKey({ path: { orgID }, query: { name: '*Support*' }, client }),
    listAgentsQueryKey(scope),
    listAgentsQueryKey({ ...scope, query: { limit: 1 } }),
    listOrgAgentsQueryKey({ path: { orgID }, client }),
    listOrgAgentsQueryKey({ path: { orgID }, query: { limit: 1 }, client }),
    listCronTriggersQueryKey(scope),
    listCronTriggersQueryKey({ ...scope, query: { integration_id: integrationID } }),
  ]
  const unaffected = [
    listOrgAgentsQueryKey({ path: { orgID: `org_${'b'.repeat(26)}` }, client }),
    listOrgIntegrationsQueryKey({ path: { orgID: `org_${'b'.repeat(26)}` }, client }),
    listAgentsQueryKey({ path: { orgID, projectID: `proj_${'b'.repeat(26)}` }, client }),
    listCronTriggersQueryKey({ path: { orgID, projectID: `proj_${'b'.repeat(26)}` }, client }),
    getAgentConfigQueryKey({
      path: { orgID, projectID, agentConfigID: fakeId('acfg') },
      client,
    }),
    listConfiguredModelsQueryKey({
      path: { orgID, modelProviderConfigID: fakeId('mpc') },
      client,
    }),
  ]
  const detail = getIntegrationQueryKey({ path: { orgID, projectID, integrationID }, client })
  for (const key of [...changed, ...unaffected, detail]) cache.setQueryData(key, {})

  try {
    act(() => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>
            <DeleteIntegration />
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
    })
    await act(async () => {
      button('Delete integration').click()
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    await waitForUI(() => {
      expect(container.textContent).toContain('Deleted')
    })
    for (const key of changed) expect(cache.getQueryState(key)?.isInvalidated).toBe(true)
    for (const key of unaffected) expect(cache.getQueryState(key)?.isInvalidated).toBe(false)
    expect(cache.getQueryState(detail)).toBeUndefined()
  } finally {
    act(() => {
      root.unmount()
    })
    cache.clear()
    container.remove()
    restore()
  }
})
