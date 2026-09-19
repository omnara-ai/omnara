/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, schemas } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import * as z from 'zod'

import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, submit, waitForUI } from '@/test/secret-editor'

import { AgentProfileApps } from './AgentProfileApps'
import { DeployAgentProfileDialog } from './DeployAgentProfileDialog'
import { ProjectAppSetupDialog } from './ProjectAppSetupDialog'
import { slackOAuthErrorDescription } from './SlackOAuthOutcomeDialogState'

let root: Root
let container: HTMLDivElement
let restore: () => void
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

it.each(['GitHub', 'Discord'] as const)(
  'retries %s app failure without duplicating credentials or the connection',
  async (label) => {
    const provider = label === 'GitHub' ? 'github' : 'discord'
    const orgId = fakeId('org'),
      projectId = fakeId('proj'),
      profileId = fakeId('aprf')
    const projectPath = `/api/v1/orgs/${orgId}/projects/${projectId}`
    const credentialPath = `/api/v1/orgs/${orgId}/secrets`
    const now = '2026-09-18T00:00:00Z'
    const connection = {
      id: fakeId('iin'),
      org_id: orgId,
      project_id: projectId,
      provider,
      provider_tenant_id: '111',
      provider_account_ref: '222',
      provider_agent_display_name: 'Support',
      state: 'active',
      provider_config: {},
      credential_secret_id: fakeId('sec'),
      created_at: now,
      updated_at: now,
    }
    let appAttempts = 0
    let connectionSaved = false
    const api = fakeApi([
      {
        method: 'GET',
        path: projectPath + '/integration-connections',
        respond: () =>
          jsonResponse({ data: connectionSaved ? [connection] : [], next_cursor: null }),
      },
      {
        method: 'GET',
        path: projectPath + '/secrets',
        respond: () => jsonResponse({ data: [], next_cursor: null }),
      },
      {
        method: 'POST',
        path: credentialPath,
        respond: () =>
          jsonResponse(
            {
              id: fakeId('sec'),
              org_id: orgId,
              name: 'Credentials',
              owner: { kind: 'project', project_id: projectId },
              kind: provider === 'github' ? 'github_app_credentials' : 'generic',
              management_kind: 'tenant',
              metadata: {},
              current_version_number: 1,
              payload_keys: [],
              created_at: now,
              updated_at: now,
            },
            201,
          ),
      },
      {
        method: 'POST',
        path: projectPath + '/integration-connections',
        respond: () => {
          connectionSaved = true
          return jsonResponse(connection, 201)
        },
      },
      {
        method: 'POST',
        path: projectPath + '/apps',
        respond: ({ body }) => {
          appAttempts++
          if (appAttempts === 1)
            return jsonResponse({ code: 'conflict', error: 'Project app capacity reached' }, 409)
          const setup = schemas.zSaveProjectAppRequest.parse(body)
          return jsonResponse(
            z.json().parse({
              ...setup,
              id: fakeId('app'),
              project_id: projectId,
              created_at: now,
              updated_at: now,
            }),
            201,
          )
        },
      },
    ])
    const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
    const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const closed = vi.fn()
    act(() => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>
            <ProjectAppSetupDialog
              open
              onOpenChange={closed}
              orgId={orgId}
              projectId={projectId}
              profile={{ id: profileId, name: 'Support' }}
            />
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
    })
    act(() => {
      button(label).click()
    })
    await enter(label === 'GitHub' ? 'GitHub App ID' : 'Discord Application ID', '111')
    await enter(label === 'GitHub' ? 'GitHub Installation ID' : 'Discord bot User ID', '222')
    await enter(label === 'GitHub' ? 'Repository ID' : 'Channel ID', '333')
    if (label === 'GitHub') {
      await enter('RSA private key (PEM)', 'test-key')
      await enter('Webhook secret', 'private-signature')
    } else {
      await enter('Bot token', 'private-token')
      await enter('Gateway shard count', '4')
      await enter('Interaction public key (optional)', 'ab'.repeat(32))
    }
    await submit()
    await waitForUI(() => {
      expect(document.body.textContent).toContain('Project app capacity reached')
    })
    expect(closed).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain(`Connection saved: ${connection.id}`)
    await submit()
    await waitForUI(() => {
      expect(closed).toHaveBeenCalledWith(false)
    })
    expect(api.requestsTo('POST', credentialPath)).toHaveLength(1)
    expect(api.requestsTo('POST', projectPath + '/integration-connections')).toHaveLength(1)
    expect(api.requestsTo('POST', projectPath + '/apps')).toHaveLength(2)
    expect(api.requestsTo('POST', projectPath + '/apps')[1]?.body).toMatchObject({
      settings: {
        resource: { definition: `omnara.${provider}`, connection: connection.id },
        launcher: {
          scope_kind: provider === 'github' ? 'repository' : 'channel',
          scope_ref: '333',
          slots: [{ agent_profile_id: profileId }],
        },
      },
    })
    if (provider === 'discord')
      expect(
        api.requestsTo('POST', projectPath + '/integration-connections')[0]?.body,
      ).toMatchObject({
        provider_tenant_id: '111',
        provider_account_ref: '222',
        provider_config: { public_key: 'ab'.repeat(32), shard_count: 4 },
      })
  },
)

it('explains quota/setup save failure without suggesting a duplicate Slack registration', () => {
  expect(slackOAuthErrorDescription('setup_save_failed')).toContain('project app limits')
  expect(slackOAuthErrorDescription('setup_save_failed')).not.toContain('already connected')
})

it('submits existing Slack credentials to OAuth and keeps provider errors actionable', async () => {
  const orgId = fakeId('org'),
    projectId = fakeId('proj'),
    profileId = fakeId('aprf')
  const setupPath = `/api/v1/orgs/${orgId}/projects/${projectId}/agent-profiles/${profileId}/integration-oauth/setup`
  const api = fakeApi([
    {
      method: 'POST',
      path: setupPath,
      respond: () =>
        jsonResponse(
          { code: 'invalid_request', error: 'Slack client credentials are invalid' },
          400,
        ),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={cache}>
          <DeployAgentProfileDialog
            open
            onOpenChange={() => undefined}
            orgId={orgId}
            projectId={projectId}
            profile={{ id: profileId, name: 'Support' }}
          />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })
  const checkbox = document.querySelector<HTMLInputElement>('input[type="checkbox"]')
  if (!checkbox) throw new Error('Missing existing app choice')
  act(() => {
    checkbox.click()
  })
  await enter('Client ID', 'client-id')
  await enter('Client secret', 'client-secret')
  await enter('Signing secret', 'signing-secret')
  act(() => {
    button('Continue').click()
  })
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Slack client credentials are invalid')
  })
  expect(api.requestsTo('POST', setupPath)).toHaveLength(1)
  expect(api.requestsTo('POST', setupPath)[0]?.body).toMatchObject({
    provider: 'slack',
    client_id: 'client-id',
    client_secret: 'client-secret',
    signing_secret: 'signing-secret',
  })
  expect(document.body.textContent).toContain('Reconnecting preserves existing app settings')
})

it.each([false, true])(
  'pages through project apps and preserves broken settings on disable (manage=%s)',
  async (canManage) => {
    const orgId = fakeId('org'),
      projectId = fakeId('proj'),
      profileId = fakeId('aprf')
    const path = `/api/v1/orgs/${orgId}/projects/${projectId}`
    const now = '2026-09-18T00:00:00Z'
    const settings = {
      resource: {
        definition: 'omnara.slack',
        connection: fakeId('iin'),
        tools: { slack_read: {} },
      },
      launcher: {
        trigger: 'mention',
        scope_kind: 'workspace',
        scope_ref: 'T123',
        slots: [{ key: 'default', agent_profile_id: profileId }],
      },
    }
    const app = {
      id: fakeId('app'),
      project_id: projectId,
      name: 'Helpdesk',
      enabled: true,
      settings,
      created_at: now,
      updated_at: now,
    }
    const api = fakeApi([
      {
        method: 'GET',
        path: path + '/apps',
        respond: ({ url }) =>
          jsonResponse({
            data: url.searchParams.has('cursor') ? [app] : [],
            next_cursor: url.searchParams.has('cursor') ? null : 'page-two',
          }),
      },
      {
        method: 'GET',
        path: path + '/integration-connections',
        respond: () => jsonResponse({ data: [], next_cursor: null }),
      },
      {
        method: 'GET',
        path: path + '/integration-connections/' + fakeId('iin'),
        respond: () => jsonResponse({ code: 'not_found', error: 'Connection was deleted' }, 404),
      },
      {
        method: 'PUT',
        path: path + '/apps/' + app.id,
        respond: ({ body }) => {
          expect(body).toEqual({ name: app.name, settings, enabled: false })
          app.enabled = false
          return jsonResponse(app)
        },
      },
    ])
    const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
    const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    act(() => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>
            <AgentProfileApps
              orgId={orgId}
              projectId={projectId}
              profileId={profileId}
              canManage={canManage}
            />
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
    })
    await waitForUI(() => {
      expect(document.body.textContent).toContain('No apps for this profile on the loaded pages')
    })
    act(() => {
      button('Show more').click()
    })
    await waitForUI(() => {
      expect(document.body.textContent).toContain('Connection unavailable')
    })
    expect(document.body.textContent).toContain('Helpdesk')
    expect(
      api
        .requestsTo('GET', path + '/apps')
        .at(-1)
        ?.url.searchParams.get('cursor'),
    ).toBe('page-two')
    if (canManage) {
      act(() => {
        button('Disable app').click()
      })
      await waitForUI(() => {
        expect(document.body.textContent).toContain('Enable app')
      })
      expect(api.requestsTo('PUT', path + '/apps/' + app.id)).toHaveLength(1)
    } else {
      expect(document.body.textContent).not.toContain('Disable app')
      expect(document.body.textContent).not.toContain('Remove')
      expect(document.body.textContent).not.toContain('Disconnect')
    }
  },
)
