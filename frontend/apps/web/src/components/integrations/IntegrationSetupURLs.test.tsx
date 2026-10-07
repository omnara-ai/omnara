/** @vitest-environment happy-dom */
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { fakeApi } from '@/test/fake-api'
import { fakeId, integration } from '@/test/fixtures'
import { renderIntegration } from '@/test/integration-render'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, field, waitForUI } from '@/test/secret-editor'

import { ConnectSlackForm } from './ConnectSlackForm'
import { IntegrationAdvanced } from './IntegrationAdvanced'
import { IntegrationPortalSetup } from './IntegrationPortalSetup'

let root: Root, container: HTMLDivElement, restore: () => void
let clearCache: (() => void) | undefined
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
  clearCache?.()
  container.remove()
  restore()
  vi.unstubAllGlobals()
})

function render(content: ReactNode, config: { api_url?: string; public_url?: string }) {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json(config)))
  const { cache } = renderIntegration(root, fakeApi([]), content, null)
  clearCache = () => {
    cache.clear()
  }
}

it('uses the configured API origin for the shared GitHub webhook before an App ID exists', async () => {
  render(<IntegrationPortalSetup integrationKind="github_pr" providerId="" />, {
    api_url: 'https://public-api.example/api/v1',
    public_url: 'https://public-dashboard.example',
  })
  await waitForUI(() => {
    expect(field('Webhook URL').value).toBe(
      'https://public-api.example/api/integrations/github/events',
    )
  })
  expect(container.textContent).toContain('pull_request_review,')
})

it('uses the configured API origin for Discord interactions', async () => {
  render(<IntegrationPortalSetup integrationKind="discord_thread" providerId="123" />, {
    api_url: 'https://public-api.example/api/v1',
  })
  await waitForUI(() => {
    expect(field('Interactions Endpoint URL').value).toBe(
      'https://public-api.example/api/integrations/discord/123/interactions',
    )
  })
  expect(container.textContent).toContain('even without an Omnara account')
})

it('uses the configured public URL when the API shares the dashboard origin', async () => {
  render(<IntegrationPortalSetup integrationKind="github_pr" />, {
    public_url: 'https://public-dashboard.example/',
  })
  await waitForUI(() => {
    expect(field('Webhook URL').value).toBe(
      'https://public-dashboard.example/api/integrations/github/events',
    )
  })
})

it('does not offer a browser-origin webhook when public configuration is missing', async () => {
  render(<IntegrationPortalSetup integrationKind="github_pr" />, {})
  await waitForUI(() => {
    expect(field('Webhook URL').placeholder).toBe('Public API URL unavailable')
  })
  expect(field('Webhook URL').value).toBe('')
  expect(button('Copy').disabled).toBe(true)
})

it('shows all four events and the shared URL in advanced GitHub setup', async () => {
  render(<IntegrationAdvanced integration={integration({ integration_kind: 'github_pr' })} />, {
    api_url: 'https://public-api.example/api/v1',
  })
  act(() => {
    button('Advanced').click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain(
      'https://public-api.example/api/integrations/github/events',
    )
  })
  expect(container.textContent).toContain('pull request reviews,')
})

it.each([
  { providerId: undefined, expectedId: 'APPLICATION_ID' },
  { providerId: '', expectedId: 'APPLICATION_ID' },
  { providerId: '123', expectedId: '123' },
])(
  'shows the Discord endpoint with a usable application ID: %j',
  async ({ providerId, expectedId }) => {
    render(
      <IntegrationAdvanced
        integration={integration({
          integration_kind: 'discord_thread',
          provider_tenant_id: providerId,
        })}
      />,
      { api_url: 'https://public-api.example/api/v1' },
    )
    act(() => {
      button('Advanced').click()
    })
    await waitForUI(() => {
      expect(container.textContent).toContain(
        `https://public-api.example/api/integrations/discord/${expectedId}/interactions`,
      )
    })
  },
)

it('uses the public dashboard URL for all Slack callbacks even with a separate API origin', async () => {
  render(<ConnectSlackForm orgId={fakeId('org')} projectId={fakeId('proj')} />, {
    api_url: 'https://public-api.example/api/v1',
    public_url: 'https://public-dashboard.example',
  })
  act(() => {
    button('Enter app details').click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain(
      'https://public-dashboard.example/api/integrations/oauth/callback',
    )
  })
  expect(container.textContent).toContain(
    'https://public-dashboard.example/api/integrations/slack/events',
  )
  expect(container.textContent).toContain(
    'https://public-dashboard.example/api/integrations/slack/actions',
  )
})
