/** @vitest-environment happy-dom */
import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId, integration as integrationFixture } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, field, waitForUI } from '@/test/secret-editor'

import { ConnectSlackForm } from './ConnectSlackForm'

let root: Root, container: HTMLDivElement, cache: QueryClient, restore: () => void
beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
  cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  cache.setQueryDefaults(['web-config'], { staleTime: Infinity })
  cache.setQueryData(['web-config'], { publicURL: 'https://omnara.test' })
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  cache.clear()
  container.remove()
  restore()
  window.history.replaceState(null, '', '/')
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

it('authorizes in the same tab without polling and restarts an expired flow with the existing app', async () => {
  vi.useFakeTimers()
  const orgId = fakeId('org'),
    projectId = fakeId('proj')
  const integration = integrationFixture()
  const path = `/api/v1/orgs/${orgId}/projects/${projectId}/integrations/${integration.id}`
  const setup = () => ({
    integration_id: integration.id,
    setup_revision: integration.setup_revision,
    provider: 'slack',
    slack_app_id: 'A123',
    flow_id: fakeId('ioaf'),
    oauth_url: 'https://slack.com/oauth/v2/authorize',
    redirect_uri: 'https://omnara.test/callback',
    events_url: 'https://omnara.test/events',
    actions_url: 'https://omnara.test/actions',
    expires_at: new Date(Date.now() + 600_000).toISOString(),
  })
  const api = fakeApi([
    { method: 'POST', path: path + '/slack-setup', respond: () => jsonResponse(setup(), 201) },
    { method: 'POST', path: path + '/oauth/setup', respond: () => jsonResponse(setup(), 201) },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={cache}>
          <ConnectSlackForm integration={integration} orgId={orgId} projectId={projectId} />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })
  expect(field('Name in Slack').value).toBe('')
  expect(field('Name in Slack').hasAttribute('placeholder')).toBe(true)
  expect(field('Name in Slack').required).toBe(true)
  await enter('App configuration token', 'config-token')
  expect(button('Connect integration').disabled).toBe(true)
  await enter('Name in Slack', 'Engineering helper')
  act(() => {
    button('Connect integration').click()
  })
  await waitForUI(() => {
    const link = container.querySelector('a[href="https://slack.com/oauth/v2/authorize"]')
    expect(link).not.toBeNull()
    expect(link?.getAttribute('target')).toBeNull()
  })
  expect(api.requestsTo('POST', path + '/slack-setup')[0]?.body).toMatchObject({
    app_name: 'Engineering helper',
    return_to: `/projects/${projectId}/integrations/${integration.id}`,
  })
  await act(async () => {
    await vi.advanceTimersByTimeAsync(600_000)
  })
  expect(container.querySelector('[role="alert"]')?.textContent).toContain('Authorization expired')
  expect(container.querySelector('a[href="https://slack.com/oauth/v2/authorize"]')).toBeNull()
  expect(api.requestsTo('GET', path)).toHaveLength(0)
  act(() => {
    button('Start authorization again').click()
  })
  expect(container.querySelector<HTMLInputElement>('input[type="checkbox"]')?.checked).toBe(true)
  await enter('Client ID', 'client')
  await enter('Client secret', 'secret')
  await enter('Signing secret', 'signature')
  act(() => {
    button('Connect integration').click()
  })
  await waitForUI(() => {
    expect(container.querySelector('a[href="https://slack.com/oauth/v2/authorize"]')).not.toBeNull()
  })
  expect(api.requestsTo('POST', path + '/slack-setup')).toHaveLength(1)
  expect(api.requestsTo('POST', path + '/oauth/setup')).toHaveLength(1)
  expect(container.querySelector('[role="alert"]')).toBeNull()
})

it('explains an integration creation conflict before creating the Slack app', async () => {
  const orgId = fakeId('org'),
    projectId = fakeId('proj')
  const projectPath = `/api/v1/orgs/${orgId}/projects/${projectId}`
  const api = fakeApi([
    {
      method: 'POST',
      path: projectPath + '/integrations',
      respond: () =>
        jsonResponse({ code: 'conflict', error: 'Integration name already exists' }, 409),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={cache}>
          <ConnectSlackForm orgId={orgId} projectId={projectId} />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })
  await enter('Name in Slack', 'Reviewer')
  await enter('App configuration token', 'config-token')
  await act(async () => {
    document
      .querySelector('form')
      ?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await Promise.resolve()
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="alert"]')?.textContent).toBe(
      'Integration name already exists. If you started setup earlier, open it from Integrations to continue.',
    )
  })
  expect(api.requests.filter((request) => request.method === 'POST')).toHaveLength(1)
})

it('keeps focus and each Slack setup draft, including the icon, when switching methods', async () => {
  vi.stubGlobal(
    'createImageBitmap',
    vi.fn().mockResolvedValue({ width: 512, height: 512, close: vi.fn() }),
  )
  const orgId = fakeId('org'),
    projectId = fakeId('proj')
  const integration = integrationFixture()
  const path = `/api/v1/orgs/${orgId}/projects/${projectId}/integrations/${integration.id}`
  const api = fakeApi([
    {
      method: 'POST',
      path: path + '/slack-setup',
      respond: () =>
        jsonResponse(
          {
            integration_id: integration.id,
            setup_revision: integration.setup_revision,
            provider: 'slack',
            slack_app_id: 'A123',
            flow_id: fakeId('ioaf'),
            oauth_url: 'https://slack.com/oauth/v2/authorize',
            redirect_uri: 'https://omnara.test/callback',
            events_url: 'https://omnara.test/events',
            actions_url: 'https://omnara.test/actions',
            expires_at: new Date(Date.now() + 600_000).toISOString(),
          },
          201,
        ),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={cache}>
          <ConnectSlackForm integration={integration} orgId={orgId} projectId={projectId} />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })
  const methodToggle = field('Use an existing Slack app')
  function toggleExistingApp() {
    methodToggle.focus()
    act(() => {
      methodToggle.click()
    })
    expect(field('Use an existing Slack app')).toBe(methodToggle)
    expect(document.activeElement).toBe(methodToggle)
  }
  await enter('Name in Slack', 'Reviewer')
  await enter('App configuration token', 'config-token')
  act(() => {
    const transfer = new DataTransfer()
    transfer.items.add(new File(['image'], 'icon.png', { type: 'image/png' }))
    const input = field('Slack app icon (optional)')
    if (!(input instanceof HTMLInputElement)) throw new Error('Expected file input')
    input.files = transfer.files
    input.dispatchEvent(new Event('change', { bubbles: true }))
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('icon.png')
  })
  toggleExistingApp()
  await enter('Client ID', 'client')
  toggleExistingApp()
  expect(field('Name in Slack').value).toBe('Reviewer')
  expect(field('App configuration token').value).toBe('config-token')
  expect(container.textContent).toContain('icon.png')
  toggleExistingApp()
  expect(field('Client ID').value).toBe('client')
  toggleExistingApp()
  act(() => {
    button('Connect integration').click()
  })
  await waitForUI(() => {
    expect(api.requestsTo('POST', path + '/slack-setup')).toHaveLength(1)
  })
  expect(api.requestsTo('POST', path + '/slack-setup')[0]?.body).toMatchObject({
    app_name: 'Reviewer',
    app_configuration_token: 'config-token',
    icon: { filename: 'icon.png', data_base64: 'aW1hZ2U=' },
  })
})
