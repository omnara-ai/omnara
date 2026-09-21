/** @vitest-environment happy-dom */

import { type ProjectApp, schemas } from '@omnara/sdk'
import { getProjectAppQueryKey, listProjectAppsInfiniteOptions } from '@omnara/sdk/tanstack'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { ProjectAppDetail } from '@/routes/ProjectAppPage'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId, projectApp } from '@/test/fixtures'
import { renderProjectApp } from '@/test/project-app-render'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, waitForUI } from '@/test/secret-editor'

const orgId = fakeId('org'),
  projectId = fakeId('proj')
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
  window.history.replaceState(null, '', '/')
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function render(api: ReturnType<typeof fakeApi>, content: ReactNode) {
  return renderProjectApp(root, api, content)
}

it.each(['discord_thread', 'github_pr'] as const)(
  'continues a fresh %s app from a successful configure mutation to profile editing',
  async (appType) => {
    const app = projectApp({ app_type: appType })
    const setupPath = `${projectPath}/apps/${app.id}/setup`
    const api = fakeApi([
      {
        method: 'GET',
        path: `${projectPath}/apps/${app.id}`,
        respond: () => Response.json(app),
      },
      {
        method: 'POST',
        path: `/api/v1/orgs/${orgId}/secrets`,
        respond: () =>
          Response.json(
            {
              id: fakeId('sec'),
              org_id: orgId,
              owner: { kind: 'project', project_id: projectId },
              name: 'Credentials',
              kind: appType === 'github_pr' ? 'github_app_credentials' : 'generic',
              management_kind: 'tenant',
              metadata: {},
              current_version_number: 1,
              payload_keys: [],
              created_at: app.created_at,
              updated_at: app.updated_at,
            },
            { status: 201 },
          ),
      },
      {
        method: 'POST',
        path: setupPath,
        respond: ({ body }) =>
          Response.json({
            ...app,
            ...schemas.zConfigureProjectAppRequest.parse(body),
            state: 'active',
            setup_revision: app.setup_revision + 1,
            updated_at: '2026-09-19T00:01:00Z',
          }),
      },
      ...[`apps/${app.id}/subscriptions`, 'cron-triggers', 'agent-profiles'].map((suffix) => ({
        method: 'GET',
        path: `${projectPath}/${suffix}`,
        respond: () => Response.json({ data: [], next_cursor: null }),
      })),
    ])
    render(api, <ProjectAppDetail orgId={orgId} projectId={projectId} appId={app.id} canManage />)
    await waitForUI(() => {
      expect(container.querySelector('#provider-tenant')).not.toBeNull()
    })
    await enter(appType === 'github_pr' ? 'GitHub App ID' : 'Discord Application ID', '111')
    await enter(appType === 'github_pr' ? 'Installation ID' : 'Bot User ID', '222')
    if (appType === 'github_pr') {
      await enter('RSA private key (PEM)', 'test-key')
      await enter('Webhook secret', 'signature')
    } else {
      await enter('Bot token', 'token')
      await enter('Interaction public key', 'ab'.repeat(32))
    }
    act(() => {
      button('Connect app').click()
    })
    await waitForUI(() => {
      expect(button('Save changes')).toBeDefined()
    })
    expect(api.requestsTo('POST', setupPath)).toHaveLength(1)
    expect(container.querySelector('#provider-tenant')).toBeNull()
    expect(container.textContent).toContain('Account connected.')
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    if (appType === 'discord_thread') expect(button('Add schedule').disabled).toBe(false)
    act(() => {
      button('Cancel').click()
    })
    expect(container.querySelector('form')).toBeNull()
    expect(button(appType === 'github_pr' ? 'Choose a profile' : 'Choose profiles')).toBeDefined()
  },
)

it('keeps fresh app connection controls unavailable to readers', async () => {
  const app = projectApp()
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/apps/${app.id}`,
      respond: () => Response.json(app),
    },
  ])
  render(
    api,
    <ProjectAppDetail orgId={orgId} projectId={projectId} appId={app.id} canManage={false} />,
  )
  await waitForUI(() => {
    expect(container.textContent).toContain('Ask a project administrator to connect this app.')
  })
  expect(container.querySelector('form')).toBeNull()
  expect(container.querySelector('[aria-label="App actions"]')).toBeNull()
  expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
})

it.each([true, false])(
  'opens profiles only for this app’s Slack success callback without a modal (matching=%s)',
  async (matching) => {
    const app = projectApp({
      state: 'active',
      provider_tenant_id: 'T123',
      provider_account_ref: 'A123',
    })
    const pagePath = `/projects/${projectId}/apps/${app.id}`
    window.history.replaceState(
      { checkpoint: 'kept' },
      '',
      `${pagePath}?draft=keep&integration_oauth=success&app_id=${matching ? app.id : 'another-app'}#profiles`,
    )
    const api = fakeApi([
      {
        method: 'GET',
        path: `${projectPath}/apps/${app.id}`,
        respond: () => Response.json(app),
      },
      {
        method: 'GET',
        path: `${projectPath}/apps/${app.id}/subscriptions`,
        respond: () => Response.json({ data: [], next_cursor: null }),
      },
      {
        method: 'GET',
        path: `${projectPath}/cron-triggers`,
        respond: () => Response.json({ data: [], next_cursor: null }),
      },
      {
        method: 'GET',
        path: `${projectPath}/agent-profiles`,
        respond: () => Response.json({ data: [], next_cursor: null }),
      },
    ])
    render(api, <ProjectAppDetail orgId={orgId} projectId={projectId} appId={app.id} canManage />)
    await waitForUI(() => {
      expect(button(matching ? 'Save changes' : 'Choose profiles')).toBeDefined()
    })
    expect(container.textContent.includes('Account connected.')).toBe(matching)
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(window.location.pathname + window.location.search + window.location.hash).toBe(
      `${pagePath}?draft=keep#profiles`,
    )
    expect(window.history.state).toEqual({ checkpoint: 'kept' })
  },
)

async function beginSlackAuthorization({ setupRevision = 1, callbackError = false } = {}) {
  const draft = projectApp()
  const flowId = fakeId('ioaf')
  const detailPath = `${projectPath}/apps/${draft.id}`
  const pagePath = `/projects/${projectId}/apps/${draft.id}`
  window.history.replaceState(
    null,
    '',
    pagePath + (callbackError ? '?integration_oauth_error=missing_scope' : ''),
  )
  let current = draft
  const api = fakeApi([
    { method: 'GET', path: detailPath, respond: () => Response.json(current) },
    {
      method: 'POST',
      path: `${detailPath}/slack-setup`,
      respond: () =>
        Response.json(
          {
            app_id: draft.id,
            setup_revision: setupRevision,
            provider: 'slack',
            slack_app_id: 'A123',
            flow_id: flowId,
            oauth_url: 'https://slack.test/authorize',
            redirect_uri: 'https://omnara.test/api/integrations/oauth/callback',
            events_url: 'https://omnara.test/api/integrations/slack/events',
            actions_url: 'https://omnara.test/api/integrations/slack/actions',
            expires_at: new Date(Date.now() + 600_000).toISOString(),
          },
          { status: 201 },
        ),
    },
    ...[
      `${detailPath}/subscriptions`,
      `${projectPath}/cron-triggers`,
      `${projectPath}/agent-profiles`,
    ].map((path) => ({
      method: 'GET',
      path,
      respond: () => Response.json({ data: [], next_cursor: null }),
    })),
  ])
  const context = render(
    api,
    <ProjectAppDetail orgId={orgId} projectId={projectId} appId={draft.id} canManage />,
  )
  await waitForUI(() => {
    expect(document.querySelector('#slack-app-configuration-token')).not.toBeNull()
  })
  await enter('App configuration token', 'fake-configuration-token')
  act(() => {
    button('Connect app').click()
  })
  await waitForUI(() => {
    expect(document.querySelector('a[href="https://slack.test/authorize"]')).not.toBeNull()
  })
  const queryKey = getProjectAppQueryKey({
    path: { orgID: orgId, projectID: projectId, appID: draft.id },
    client: context.client,
  })
  const connected: ProjectApp = {
    ...draft,
    state: 'active',
    provider_tenant_id: 'T123',
    provider_account_ref: 'A123',
    setup_revision: setupRevision + 1,
    last_oauth_flow_id: flowId,
    updated_at: '2026-09-19T00:01:00Z',
  }
  return {
    ...context,
    draft,
    connected,
    refreshApp: async (app: ProjectApp) => {
      current = app
      await act(async () => {
        await context.cache.invalidateQueries({ queryKey })
      })
    },
  }
}

it('keeps Slack authorization open when a fresh read reveals an earlier active flow', async () => {
  // The browser still has revision 1; the new OAuth intent captured server revision 2.
  const { connected, refreshApp } = await beginSlackAuthorization({ setupRevision: 2 })
  await refreshApp({
    ...connected,
    setup_revision: 2,
    last_oauth_flow_id: `ioaf_${'b'.repeat(26)}`,
  })
  await waitForUI(() => {
    // Wait for the page to render the refreshed app before checking the pending form.
    expect(container.querySelector('header')?.textContent).toContain('Connected')
  })
  expect(container.querySelector('a[href="https://slack.test/authorize"]')).not.toBeNull()
  expect(container.textContent).not.toContain('Account connected.')
  expect(() => button('Save changes')).toThrow('Missing button')
  await refreshApp({ ...connected, last_oauth_flow_id: `ioaf_${'b'.repeat(26)}` })
  await waitForUI(() => {
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('changed')
    expect(container.querySelector('#clientId')).not.toBeNull()
  })
  expect(container.querySelector('#slack-app-configuration-token')).toBeNull()
  expect(container.querySelector('input[type="checkbox"]')).toBeNull()
  expect(container.textContent).not.toContain('Account connected.')
  expect(() => button('Save changes')).toThrow('Missing button')
})

it('invalidates only this project’s cached app list before finishing Slack authorization', async () => {
  const { cache, client, draft, connected, refreshApp } = await beginSlackAuthorization()
  const listKey = (projectID: string) =>
    listProjectAppsInfiniteOptions({ path: { orgID: orgId, projectID }, client }).queryKey
  const currentList = listKey(projectId)
  const otherList = listKey(`proj_${'b'.repeat(26)}`)
  const cachedList = {
    pages: [{ data: [draft], next_cursor: null }],
    pageParams: [undefined],
  }
  cache.setQueryData(currentList, cachedList)
  cache.setQueryData(otherList, {
    pages: [{ data: [], next_cursor: null }],
    pageParams: [undefined],
  })
  await refreshApp(connected)
  await waitForUI(() => {
    expect(button('Save changes')).toBeDefined()
    expect(container.querySelector('a[href="https://slack.test/authorize"]')).toBeNull()
  })
  expect(cache.getQueryState(currentList)?.isInvalidated).toBe(true)
  expect(cache.getQueryState(otherList)?.isInvalidated).toBe(false)
  expect(container.textContent).toContain('Account connected.')
})

it('clears an earlier Slack callback error after a successful inline authorization retry', async () => {
  const { connected, refreshApp } = await beginSlackAuthorization({ callbackError: true })
  expect(container.textContent).toContain('Slack setup didn’t finish.')
  expect(window.location.search).toBe('')
  await refreshApp(connected)
  await waitForUI(() => {
    expect(button('Save changes')).toBeDefined()
    expect(container.textContent).toContain('Account connected.')
  })
  expect(container.textContent).not.toContain('Slack setup didn’t finish.')
  expect(container.querySelector('[role="alert"]')).toBeNull()
  act(() => {
    button('Cancel').click()
  })
  expect(container.textContent).not.toContain('Account connected.')
  expect(container.textContent).not.toContain('Slack setup didn’t finish.')
  expect(container.querySelector('[role="alert"]')).toBeNull()
})

it.each([404, 500])(
  'preserves the Slack callback error when the initial app read fails with %s',
  async (status) => {
    const app = projectApp()
    const pagePath = `/projects/${projectId}/apps/${app.id}`
    window.history.replaceState(
      { checkpoint: 'kept' },
      '',
      `${pagePath}?draft=keep&integration_oauth_error=app_deleted#profiles`,
    )
    let release!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => {
      release = resolve
    })
    const api = fakeApi([
      { method: 'GET', path: `${projectPath}/apps/${app.id}`, respond: () => pending },
    ])
    render(api, <ProjectAppDetail orgId={orgId} projectId={projectId} appId={app.id} canManage />)
    // Callback parameters are consumed even before the app response is available.
    expect(window.location.pathname + window.location.search + window.location.hash).toBe(
      `${pagePath}?draft=keep#profiles`,
    )
    expect(window.history.state).toEqual({ checkpoint: 'kept' })
    act(() => {
      release(jsonResponse({ code: 'unavailable', error: 'Unavailable' }, status))
    })
    await waitForUI(() => {
      expect(container.querySelector('[role="alert"]')?.textContent).toContain(
        'This app was deleted. Choose or create an app before starting setup again.',
      )
    })
    expect(container.textContent).toContain('Could not load this app.')
    expect(button('Retry')).toBeDefined()
    expect(container.querySelector('form')).toBeNull()
    expect(document.querySelector('[role="dialog"]')).toBeNull()
  },
)
