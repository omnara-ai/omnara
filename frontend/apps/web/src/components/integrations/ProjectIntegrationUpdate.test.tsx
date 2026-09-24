/** @vitest-environment happy-dom */

import type { AgentProfile } from '@omnara/sdk'
import { getProjectIntegrationQueryKey } from '@omnara/sdk/tanstack'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { ProjectIntegrationDetail } from '@/routes/ProjectIntegrationPage'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { agentConfigModel, fakeId, projectIntegration } from '@/test/fixtures'
import { renderProjectIntegration } from '@/test/project-integration-render'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, waitForUI } from '@/test/secret-editor'

const orgId = fakeId('org'),
  projectId = fakeId('proj'),
  profileId = fakeId('aprf')
const projectPath = `/api/v1/orgs/${orgId}/projects/${projectId}`
let root: Root, container: HTMLDivElement, restore: () => void

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
  container.remove()
  restore()
})

it.each(['same setup', 'new revision', 'disconnected'] as const)(
  'keeps confirmed launcher changes after a failed refresh and ignores an older GET (%s)',
  async (scenario) => {
    const integration = projectIntegration({
      integration_type: 'discord_thread',
      state: 'active',
      provider_tenant_id: '111',
      provider_account_ref: '222',
      provider_config: { public_key: 'ab'.repeat(32) },
      settings: {
        launcher: {
          trigger: 'mention',
          slots: [{ key: 'support', agent_profile_id: profileId }],
        },
      },
    })
    const failure = {
      message: 'Discord Gateway closed: 4014',
      retry_at: '2026-09-23T12:00:00Z',
    }
    const detail = { ...integration, runtime_failure: failure }
    const updated = projectIntegration({
      ...integration,
      settings: {},
      updated_at: '2026-09-23T11:00:00Z',
      setup_revision:
        scenario === 'new revision' ? integration.setup_revision + 1 : integration.setup_revision,
      state: scenario === 'disconnected' ? 'disconnected' : 'active',
    })
    const integrationPath = `${projectPath}/integrations/${integration.id}`
    let reads = 0
    let release!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => {
      release = resolve
    })
    const profile: AgentProfile = {
      id: profileId,
      name: 'Support',
      org_id: orgId,
      project_id: projectId,
      current_generation: 1,
      current_config_id: fakeId('acfg'),
      current_config: {
        id: fakeId('acfg'),
        org_id: orgId,
        project_id: projectId,
        model: agentConfigModel(),
        effective_definition_hash: 'hash',
        compiled_definition: {
          instruction: 'Help with this integration.',
          model: { configured_model_id: fakeId('mdl') },
        },
        created_at: integration.created_at,
      },
      created_at: integration.created_at,
      updated_at: integration.updated_at,
    }
    const api = fakeApi([
      {
        method: 'GET',
        path: integrationPath,
        respond: () => {
          if (++reads === 1) return Response.json(detail)
          if (reads === 2) return pending
          return jsonResponse({ code: 'internal_error', error: 'Refresh unavailable' }, 500)
        },
      },
      { method: 'PUT', path: integrationPath, respond: () => Response.json(updated) },
      {
        method: 'GET',
        path: `${projectPath}/agent-profiles`,
        respond: () => Response.json({ data: [profile], next_cursor: null }),
      },
      {
        method: 'GET',
        path: `${projectPath}/agent-profiles/${profileId}`,
        respond: () => Response.json(profile),
      },
      ...['cron-triggers', `integrations/${integration.id}/subscriptions`].map((resource) => ({
        method: 'GET',
        path: `${projectPath}/${resource}`,
        respond: () => Response.json({ data: [], next_cursor: null }),
      })),
    ])
    const { cache, client } = renderProjectIntegration(
      root,
      api,
      <ProjectIntegrationDetail
        orgId={orgId}
        projectId={projectId}
        integrationId={integration.id}
        canManage
      />,
    )
    const queryKey = getProjectIntegrationQueryKey({
      path: { orgID: orgId, projectID: projectId, integrationID: integration.id },
      client,
    })
    await waitForUI(() => {
      expect(container.textContent).toContain(`Connection failed: ${failure.message}`)
    })
    act(() => {
      button('Edit').click()
    })
    await waitForUI(() => {
      expect(button('Remove Support')).toBeDefined()
    })
    act(() => {
      button('Remove Support').click()
    })
    let refresh!: Promise<void>
    act(() => {
      refresh = cache.invalidateQueries({ queryKey })
    })
    await waitForUI(() => {
      expect(api.requestsTo('GET', integrationPath)).toHaveLength(2)
    })
    act(() => {
      button('Save changes').click()
    })
    await waitForUI(() => {
      expect(button('Choose profiles')).toBeDefined()
      expect(container.textContent).toContain('Could not refresh this integration.')
    })
    expect(api.requestsTo('PUT', integrationPath)[0]?.body).toEqual({
      name: integration.name,
      integration_type: integration.integration_type,
      settings: {},
    })
    await act(async () => {
      release(Response.json(detail))
      await refresh
    })
    expect(api.requestsTo('GET', integrationPath)).toHaveLength(3)
    expect(cache.getQueryData(queryKey)).toEqual({
      ...updated,
      runtime_failure: scenario === 'same setup' ? failure : undefined,
    })
    act(() => {
      button('Choose profiles').click()
    })
    await waitForUI(() => {
      expect(container.textContent).toContain('0/16 selected')
      expect(container.querySelector('[aria-label="Remove Support"]')).toBeNull()
      expect(container.textContent.includes(`Connection failed: ${failure.message}`)).toBe(
        scenario === 'same setup',
      )
    })
  },
)
