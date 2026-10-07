/** @vitest-environment happy-dom */

import { schemas } from '@omnara/sdk'
import { getIntegrationQueryKey } from '@omnara/sdk/tanstack'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { IntegrationDetail } from '@/routes/IntegrationPage'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId, integration as integrationFixture } from '@/test/fixtures'
import { renderIntegration } from '@/test/integration-render'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, choose, enter, field, waitForUI } from '@/test/secret-editor'

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
  return renderIntegration(root, api, content)
}

it.each([
  { integrationKind: 'discord_thread', reconnecting: false },
  { integrationKind: 'github_pr', reconnecting: false },
  { integrationKind: 'github_pr', reconnecting: true },
] as const)(
  'continues a $integrationKind connection to profile editing (reconnecting=$reconnecting)',
  async ({ integrationKind, reconnecting }) => {
    const integration = integrationFixture({
      integration_kind: integrationKind,
      provider_tenant_id: reconnecting ? '111' : '',
      provider_account_ref: reconnecting ? '222' : '',
    })
    const setupPath = `${projectPath}/integrations/${integration.id}/setup`
    const api = fakeApi([
      {
        method: 'GET',
        path: `${projectPath}/integrations/${integration.id}`,
        respond: () => Response.json(integration),
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
              kind: integrationKind === 'github_pr' ? 'github_app_credentials' : 'generic',
              management_kind: 'tenant',
              metadata: {},
              current_version_number: 1,
              payload_keys: [],
              created_at: integration.created_at,
              updated_at: integration.updated_at,
            },
            { status: 201 },
          ),
      },
      {
        method: 'POST',
        path: setupPath,
        respond: ({ body }) =>
          Response.json({
            ...integration,
            ...schemas.zConfigureIntegrationRequest.parse(body),
            state: 'active',
            setup_revision: integration.setup_revision + 1,
            updated_at: '2026-09-19T00:01:00Z',
          }),
      },
      ...[`integrations/${integration.id}/subscriptions`, 'cron-triggers', 'agent-profiles'].map(
        (suffix) => ({
          method: 'GET',
          path: `${projectPath}/${suffix}`,
          respond: () => Response.json({ data: [], next_cursor: null }),
        }),
      ),
    ])
    render(
      api,
      <IntegrationDetail
        orgId={orgId}
        projectId={projectId}
        integrationId={integration.id}
        canManage
      />,
    )
    if (reconnecting) {
      await waitForUI(() => {
        expect(button('Reconnect account')).toBeDefined()
      })
      act(() => {
        button('Reconnect account').click()
      })
    }
    if (integrationKind === 'github_pr' && !reconnecting) {
      await waitForUI(() => {
        expect(button('Enter App details')).toBeDefined()
      })
      act(() => {
        button('Enter App details').click()
      })
    }
    await waitForUI(() => {
      expect(container.querySelector('#provider-tenant')).not.toBeNull()
    })
    if (integrationKind === 'discord_thread')
      expect(container.querySelector('#provider-endpoint')).toBeNull()
    await enter(integrationKind === 'github_pr' ? 'GitHub App ID' : 'Discord Application ID', '111')
    if (integrationKind === 'github_pr') await enter('Installation ID', '222')
    if (integrationKind === 'github_pr') {
      await enter('RSA private key (PEM)', 'test-key')
      await enter('Webhook secret', 'signature')
    } else {
      await enter('Bot token', 'token')
      await enter('Public key', 'ab'.repeat(32))
    }
    act(() => {
      button(reconnecting ? 'Reconnect integration' : 'Connect integration').click()
    })
    await waitForUI(() => {
      expect(button('Save changes')).toBeDefined()
      expect(container.textContent).toContain('Account connected.')
    })
    expect(api.requestsTo('POST', setupPath)).toHaveLength(1)
    expect(container.querySelector('#provider-tenant')).toBeNull()
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    if (integrationKind === 'github_pr') {
      expect(field('PR opened')).toHaveProperty('checked', !reconnecting)
      expect(field('Bot mentioned')).toHaveProperty('checked', !reconnecting)
    }
    if (integrationKind === 'discord_thread') {
      expect(button('Add schedule').disabled).toBe(false)
      expect(container.querySelector<HTMLInputElement>('#provider-endpoint')?.value).toBe(
        'https://omnara.test/api/integrations/discord/111/interactions',
      )
      const link = container.querySelector<HTMLAnchorElement>('a[href*="oauth2/authorize"]')
      if (!link) throw new Error('Missing Discord invitation link')
      const invite = new URL(link.href)
      expect(invite.searchParams.get('client_id')).toBe('111')
      expect(invite.searchParams.get('scope')).toBe('bot')
      expect(invite.searchParams.get('integration_type')).toBe('0')
      expect(BigInt(invite.searchParams.get('permissions') ?? '')).toBe(
        [6n, 10n, 11n, 15n, 16n, 35n, 38n].reduce((mask, bit) => mask | (1n << bit), 0n),
      )
    }
    act(() => {
      button('Cancel').click()
    })
    expect(container.querySelector('form')).toBeNull()
    expect(
      button(integrationKind === 'github_pr' ? 'Choose a profile' : 'Choose profiles'),
    ).toBeDefined()
  },
)

it('shows a saved GitHub callback credential when another setup already connected the integration', async () => {
  const integration = integrationFixture({
    integration_kind: 'github_pr',
    state: 'active',
    setup_revision: 3,
    provider_tenant_id: '999',
    provider_account_ref: '888',
  })
  const secretId = fakeId('sec')
  window.history.replaceState(
    null,
    '',
    `/projects/${projectId}/integrations/${integration.id}?github_setup=credentials_saved&github_setup_error=integration_setup_changed&credential_secret_id=${secretId}`,
  )
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/integrations/${integration.id}`,
      respond: () => Response.json(integration),
    },
    ...['secrets', 'agent-profiles', `integrations/${integration.id}/subscriptions`].map(
      (suffix) => ({
        method: 'GET',
        path: `${projectPath}/${suffix}`,
        respond: () => Response.json({ data: [], next_cursor: null }),
      }),
    ),
  ])
  render(
    api,
    <IntegrationDetail
      orgId={orgId}
      projectId={projectId}
      integrationId={integration.id}
      canManage
    />,
  )
  await waitForUI(() => {
    expect(button('Check GitHub access')).toBeDefined()
  })
  expect(container.textContent).toContain('Review the current integration setup before connecting')
  expect(container.querySelector('#saved-secret')?.textContent).toBe(secretId)
  act(() => {
    button('Enter App details').click()
  })
  expect(container.querySelector<HTMLInputElement>('#provider-tenant')?.value).toBe('999')
  expect(container.querySelector<HTMLInputElement>('#provider-tenant')?.readOnly).toBe(true)
  expect(container.querySelector<HTMLInputElement>('#provider-account')?.value).toBe('888')
  expect(container.querySelector<HTMLInputElement>('#provider-account')?.readOnly).toBe(true)
  expect(container.querySelector('#saved-secret')?.textContent).toBe(secretId)
  expect(api.requests.filter((request) => request.method === 'POST')).toHaveLength(0)
})

it.each(['guided', 'manual'] as const)(
  'locks %s GitHub setup dropdowns, including an open menu, while the integration is deleted',
  async (mode) => {
    vi.stubGlobal('confirm', () => true)
    const integration = integrationFixture({ integration_kind: 'github_pr' })
    const detailPath = `${projectPath}/integrations/${integration.id}`
    const secret = {
      id: fakeId('sec'),
      org_id: orgId,
      owner: { kind: 'project', project_id: projectId },
      name: 'Reviewer credentials',
      kind: 'github_app_credentials',
      management_kind: 'tenant',
      metadata: {},
      current_version_number: 1,
      payload_keys: [],
      created_at: integration.created_at,
      updated_at: integration.updated_at,
    }
    let release!: (response: Response) => void
    const deletion = new Promise<Response>((resolve) => {
      release = resolve
    })
    const api = fakeApi([
      { method: 'GET', path: detailPath, respond: () => Response.json(integration) },
      { method: 'DELETE', path: detailPath, respond: () => deletion },
      {
        method: 'GET',
        path: `${projectPath}/secrets`,
        respond: () =>
          Response.json({
            data: [{ secret, availability: { source: 'direct', project_id: projectId } }],
            next_cursor: null,
          }),
      },
      ...['agent-profiles', 'cron-triggers', `integrations/${integration.id}/subscriptions`].map(
        (suffix) => ({
          method: 'GET',
          path: `${projectPath}/${suffix}`,
          respond: () => Response.json({ data: [], next_cursor: null }),
        }),
      ),
    ])
    render(
      api,
      <IntegrationDetail
        orgId={orgId}
        projectId={projectId}
        integrationId={integration.id}
        canManage
      />,
    )
    await waitForUI(() => {
      expect(button('Enter App details')).toBeDefined()
    })
    if (mode === 'manual') {
      act(() => {
        button('Enter App details').click()
      })
      act(() => {
        container.querySelector<HTMLInputElement>('input[type="checkbox"]')?.click()
      })
    }
    const [label, id, idle, choice, submit] =
      mode === 'guided'
        ? [
            'GitHub App owner',
            '#github-owner',
            'Personal account',
            'Organization',
            'Continue to GitHub',
          ]
        : [
            'Saved credential',
            '#saved-secret',
            'Choose a credential',
            'Reviewer credentials',
            'Connect integration',
          ]
    const trigger = () => container.querySelector<HTMLButtonElement>(id)
    const option = () =>
      [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(
        (item) => item.textContent === choice,
      )
    await act(async () => {
      trigger()?.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
      await Promise.resolve()
    })
    await waitForUI(() => {
      expect(option()).toBeDefined()
    })
    act(() => {
      button('Delete integration').click()
    })
    await waitForUI(() => {
      expect(trigger()?.disabled).toBe(true)
    })
    expect(api.requestsTo('DELETE', detailPath)).toHaveLength(1)
    expect(option()?.getAttribute('aria-disabled')).toBe('true')
    act(() => {
      option()?.click()
    })
    expect(trigger()?.textContent).toBe(idle)
    expect(button(submit).hasAttribute('aria-busy')).toBe(false)
    act(() => {
      release(jsonResponse({ code: 'internal_error', error: 'Try again' }, 500))
    })
    await waitForUI(() => {
      expect(trigger()?.disabled).toBe(false)
    })
    await choose(label, choice)
    expect(trigger()?.textContent).toBe(choice)
  },
)

it('keeps fresh integration connection controls unavailable to readers', async () => {
  const integration = integrationFixture()
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/integrations/${integration.id}`,
      respond: () => Response.json(integration),
    },
  ])
  render(
    api,
    <IntegrationDetail
      orgId={orgId}
      projectId={projectId}
      integrationId={integration.id}
      canManage={false}
    />,
  )
  await waitForUI(() => {
    expect(container.textContent).toContain(
      'Ask a project administrator to connect this integration.',
    )
  })
  expect(container.querySelector('form')).toBeNull()
  expect(container.querySelector('[aria-label="Integration actions"]')).toBeNull()
  expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
})

it('keeps a reconnect draft through refresh failures and external activation until canceled', async () => {
  const integration = integrationFixture({
    integration_kind: 'discord_thread',
    provider_tenant_id: '111',
    provider_config: { public_key: 'ab'.repeat(32) },
  })
  const detailPath = `${projectPath}/integrations/${integration.id}`
  let current = integration
  let unavailable = false
  const api = fakeApi([
    {
      method: 'GET',
      path: detailPath,
      respond: () =>
        unavailable
          ? jsonResponse({ code: 'internal_error', error: 'Temporary failure' }, 500)
          : Response.json(current),
    },
    ...[
      'secrets',
      'agent-profiles',
      'cron-triggers',
      `integrations/${integration.id}/subscriptions`,
    ].map((suffix) => ({
      method: 'GET',
      path: `${projectPath}/${suffix}`,
      respond: () => Response.json({ data: [], next_cursor: null }),
    })),
  ])
  const { cache, client } = render(
    api,
    <IntegrationDetail
      orgId={orgId}
      projectId={projectId}
      integrationId={integration.id}
      canManage
    />,
  )
  await waitForUI(() => {
    expect(button('Reconnect account')).toBeDefined()
  })
  act(() => {
    button('Reconnect account').click()
  })
  await enter('Bot token', 'draft-token')
  const form = container.querySelector('form')
  expect(form).not.toBeNull()
  expect([...(form?.querySelectorAll('button') ?? [])]).toContain(button('Delete integration'))
  const queryKey = getIntegrationQueryKey({
    path: { orgID: orgId, projectID: projectId, integrationID: integration.id },
    client,
  })
  unavailable = true
  await act(async () => {
    await cache.invalidateQueries({ queryKey })
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('Could not refresh this integration.')
  })
  expect(container.querySelector('form')).toBe(form)
  expect(container.querySelector<HTMLInputElement>('[name="botToken"]')?.value).toBe('draft-token')
  unavailable = false
  current = { ...integration, state: 'active', setup_revision: integration.setup_revision + 1 }
  act(() => {
    button('Retry refresh').click()
  })
  await waitForUI(() => {
    expect(container.querySelector('header')?.textContent).toContain('Connected')
  })
  expect(container.querySelector('form')).toBe(form)
  expect(container.querySelector<HTMLInputElement>('[name="botToken"]')?.value).toBe('draft-token')
  expect(container.textContent).not.toContain('Account connected.')
  act(() => {
    button('Cancel').click()
  })
  expect(container.querySelector('form')).toBeNull()
  expect(button('Delete integration').closest('form')).toBeNull()
  expect(button('Choose profiles')).toBeDefined()
  expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
})

it.each([true, false])(
  'opens profiles only for this integration’s Slack success callback without a modal (matching=%s)',
  async (matching) => {
    const integration = integrationFixture({
      state: 'active',
      provider_tenant_id: 'T123',
      provider_account_ref: 'A123',
    })
    const pagePath = `/projects/${projectId}/integrations/${integration.id}`
    window.history.replaceState(
      { checkpoint: 'kept' },
      '',
      `${pagePath}?draft=keep&integration_oauth=success&integration_id=${matching ? integration.id : 'another-integration'}#profiles`,
    )
    const api = fakeApi([
      {
        method: 'GET',
        path: `${projectPath}/integrations/${integration.id}`,
        respond: () => Response.json(integration),
      },
      {
        method: 'GET',
        path: `${projectPath}/integrations/${integration.id}/subscriptions`,
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
    render(
      api,
      <IntegrationDetail
        orgId={orgId}
        projectId={projectId}
        integrationId={integration.id}
        canManage
      />,
    )
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

it.each(['access_denied', 'identity_mismatch', 'integration_setup_changed'])(
  'recovers from a %s callback in the saved integration and resumes after the next return',
  async (outcome) => {
    const integration = integrationFixture({
      state: 'active',
      name: 'existing-team-bot',
      provider_tenant_id: 'T123',
      provider_account_ref: 'A123',
      settings: { launcher: { profiles: [fakeId('aprf')] } },
    })
    const detailPath = `${projectPath}/integrations/${integration.id}`
    const pagePath = `/projects/${projectId}/integrations/${integration.id}`
    window.history.replaceState(null, '', `${pagePath}?integration_oauth_error=${outcome}`)
    const api = fakeApi([
      { method: 'GET', path: detailPath, respond: () => Response.json(integration) },
      {
        method: 'POST',
        path: detailPath + '/oauth/setup',
        respond: () =>
          jsonResponse(
            {
              integration_id: integration.id,
              setup_revision: integration.setup_revision,
              provider: 'slack',
              flow_id: fakeId('ioaf'),
              oauth_url: 'https://slack.test/authorize',
              redirect_uri: 'https://omnara.test/callback',
              events_url: 'https://omnara.test/events',
              actions_url: 'https://omnara.test/actions',
              expires_at: new Date(Date.now() + 600_000).toISOString(),
            },
            201,
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
    const detail = (
      <IntegrationDetail
        orgId={orgId}
        projectId={projectId}
        integrationId={integration.id}
        canManage
      />
    )
    const { rerender } = render(api, detail)
    await waitForUI(() => {
      expect(container.textContent).toContain('Slack setup didn’t finish.')
    })
    expect(container.textContent).not.toContain('Account connected.')
    expect(window.location.search).toBe('')
    act(() => {
      button('Integration actions').dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }),
      )
    })
    await waitForUI(() => {
      expect(document.querySelector('[role="menuitem"]')).not.toBeNull()
    })
    const reconnect = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
      (item) => item.textContent.trim() === 'Reconnect account',
    )
    if (!reconnect) throw new Error('Missing reconnect action')
    act(() => {
      reconnect.click()
    })
    await enter('Client ID', 'client')
    await enter('Client secret', 'secret')
    await enter('Signing secret', 'signature')
    act(() => {
      button('Reconnect integration').click()
    })
    await waitForUI(() => {
      expect(container.querySelector('a[href="https://slack.test/authorize"]')).not.toBeNull()
    })
    expect(api.requestsTo('POST', detailPath + '/oauth/setup')[0]?.body).toMatchObject({
      return_to: pagePath,
    })
    expect(api.requestsTo('POST', detailPath + '/slack-setup')).toHaveLength(0)
    rerender(null)
    window.history.replaceState(
      null,
      '',
      `${pagePath}?integration_oauth=success&integration_id=${integration.id}`,
    )
    rerender(detail)
    await waitForUI(() => {
      expect(container.textContent).toContain('Account connected.')
    })
    expect(container.textContent).not.toContain('Slack setup didn’t finish.')
    expect(container.querySelector('form')).toBeNull()
    expect(container.textContent).toContain('existing-team-bot')
    expect(api.requestsTo('PUT', detailPath)).toHaveLength(0)
    expect(window.location.search).toBe('')
  },
)

it.each(['access_denied', 'flow_expired'])(
  'reuses the Slack app after an initial draft returns with %s',
  async (outcome) => {
    const integration = integrationFixture({
      state: 'disconnected',
      name: 'new-team-bot',
      provider_tenant_id: '',
      provider_account_ref: '',
    })
    const detailPath = `${projectPath}/integrations/${integration.id}`
    const pagePath = `/projects/${projectId}/integrations/${integration.id}`
    window.history.replaceState(null, '', pagePath)
    const setup = {
      integration_id: integration.id,
      setup_revision: integration.setup_revision,
      provider: 'slack',
      flow_id: fakeId('ioaf'),
      oauth_url: 'https://slack.test/authorize',
      redirect_uri: 'https://omnara.test/callback',
      events_url: 'https://omnara.test/events',
      actions_url: 'https://omnara.test/actions',
      expires_at: new Date(Date.now() + 600_000).toISOString(),
    }
    const api = fakeApi([
      { method: 'GET', path: detailPath, respond: () => Response.json(integration) },
      {
        method: 'POST',
        path: detailPath + '/slack-setup',
        respond: () => jsonResponse({ ...setup, slack_app_id: 'A123' }, 201),
      },
      {
        method: 'POST',
        path: detailPath + '/oauth/setup',
        respond: () => jsonResponse(setup, 201),
      },
      ...[`${detailPath}/subscriptions`, `${projectPath}/cron-triggers`].map((path) => ({
        method: 'GET',
        path,
        respond: () => Response.json({ data: [], next_cursor: null }),
      })),
    ])
    const detail = (
      <IntegrationDetail
        orgId={orgId}
        projectId={projectId}
        integrationId={integration.id}
        canManage
      />
    )
    const { rerender } = render(api, detail)
    await waitForUI(() => {
      expect(container.querySelector('#slack-app-name')).not.toBeNull()
    })
    await enter('Name in Slack', 'Team helper')
    await enter('App configuration token', 'configuration-token')
    act(() => {
      button('Connect integration').click()
    })
    await waitForUI(() => {
      expect(container.querySelector('a[href="https://slack.test/authorize"]')).not.toBeNull()
    })
    expect(api.requestsTo('POST', detailPath + '/slack-setup')).toHaveLength(1)

    rerender(null)
    window.history.replaceState(null, '', `${pagePath}?integration_oauth_error=${outcome}`)
    rerender(detail)
    await waitForUI(() => {
      expect(container.querySelector('[role="alert"]')?.textContent).toContain(
        'Slack setup didn’t finish.',
      )
      expect(container.querySelector<HTMLInputElement>('input[type="checkbox"]')?.checked).toBe(
        true,
      )
      expect(container.querySelector('#clientId')).not.toBeNull()
    })
    expect(window.location.search).toBe('')
    expect(container.querySelector('#slack-app-name')).toBeNull()
    expect(container.querySelector('#slack-app-configuration-token')).toBeNull()
    expect(button('Connect integration').disabled).toBe(true)
    await enter('Client ID', 'original-client')
    await enter('Client secret', 'original-secret')
    await enter('Signing secret', 'original-signature')
    act(() => {
      button('Connect integration').click()
    })
    await waitForUI(() => {
      expect(container.querySelector('a[href="https://slack.test/authorize"]')).not.toBeNull()
    })
    expect(api.requestsTo('POST', detailPath + '/oauth/setup')).toHaveLength(1)
    expect(api.requestsTo('POST', detailPath + '/oauth/setup')[0]?.body).toEqual({
      client_id: 'original-client',
      client_secret: 'original-secret',
      signing_secret: 'original-signature',
      return_to: pagePath,
    })
    expect(api.requestsTo('POST', detailPath + '/slack-setup')).toHaveLength(1)
    expect(api.requestsTo('POST', `${projectPath}/integrations`)).toHaveLength(0)
  },
)

it.each([404, 500])(
  'preserves the Slack callback error when the initial integration read fails with %s',
  async (status) => {
    const integration = integrationFixture()
    const pagePath = `/projects/${projectId}/integrations/${integration.id}`
    window.history.replaceState(
      { checkpoint: 'kept' },
      '',
      `${pagePath}?draft=keep&integration_oauth_error=integration_deleted#profiles`,
    )
    let release!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => {
      release = resolve
    })
    const api = fakeApi([
      {
        method: 'GET',
        path: `${projectPath}/integrations/${integration.id}`,
        respond: () => pending,
      },
    ])
    render(
      api,
      <IntegrationDetail
        orgId={orgId}
        projectId={projectId}
        integrationId={integration.id}
        canManage
      />,
    )
    expect(window.location.pathname + window.location.search + window.location.hash).toBe(
      `${pagePath}?draft=keep#profiles`,
    )
    expect(window.history.state).toEqual({ checkpoint: 'kept' })
    act(() => {
      release(jsonResponse({ code: 'unavailable', error: 'Unavailable' }, status))
    })
    await waitForUI(() => {
      expect(container.querySelector('[role="alert"]')?.textContent).toContain(
        'This integration was deleted. Choose or create an integration before starting setup again.',
      )
    })
    expect(container.textContent).toContain('Could not load this integration.')
    expect(button('Retry')).toBeDefined()
    expect(container.querySelector('form')).toBeNull()
    expect(document.querySelector('[role="dialog"]')).toBeNull()
  },
)
