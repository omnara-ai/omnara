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

it('uses the public URL for the shared GitHub webhook even with a separate API origin', async () => {
  render(<IntegrationPortalSetup integrationKind="github_pr" providerId="" />, {
    api_url: 'https://public-api.example/api/v1',
    public_url: 'https://public-dashboard.example',
  })
  await waitForUI(() => {
    expect(field('Webhook URL').value).toBe(
      'https://public-dashboard.example/api/integrations/github/events',
    )
  })
  expect(container.textContent).toContain('pull_request_review,')
})

it('uses the public URL for Discord interactions even with a separate API origin', async () => {
  render(<IntegrationPortalSetup integrationKind="discord_thread" providerId="123" />, {
    api_url: 'https://public-api.example/api/v1',
    public_url: 'https://public-dashboard.example',
  })
  await waitForUI(() => {
    expect(field('Interactions Endpoint URL').value).toBe(
      'https://public-dashboard.example/api/integrations/discord/123/interactions',
    )
  })
  expect(container.textContent).toContain('even without an Omnara account')
})

it.each([
  ['github_pr', 'Webhook URL', 'github/events'],
  ['discord_thread', 'Interactions Endpoint URL', 'discord/123/interactions'],
] as const)(
  'uses only the public URL for %s when no API URL is configured',
  async (kind, label, path) => {
    render(<IntegrationPortalSetup integrationKind={kind} providerId="123" />, {
      public_url: 'https://public-dashboard.example/',
    })
    await waitForUI(() => {
      expect(field(label).value).toBe(`https://public-dashboard.example/api/integrations/${path}`)
    })
  },
)

it.each([{}, { api_url: 'https://public-api.example/api/v1' }])(
  'does not offer a webhook when the public URL is missing: %j',
  async (config) => {
    render(<IntegrationPortalSetup integrationKind="github_pr" />, config)
    await waitForUI(() => {
      expect(field('Webhook URL').placeholder).toBe('Public URL unavailable')
    })
    expect(field('Webhook URL').value).toBe('')
    expect(button('Copy').disabled).toBe(true)
  },
)

it('shows all four events and the public URL in advanced GitHub setup with a separate API origin', async () => {
  render(<IntegrationAdvanced integration={integration({ integration_kind: 'github_pr' })} />, {
    api_url: 'https://public-api.example/api/v1',
    public_url: 'https://public-dashboard.example',
  })
  act(() => {
    button('Advanced').click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain(
      'https://public-dashboard.example/api/integrations/github/events',
    )
  })
  expect(container.textContent).toContain('pull request reviews,')
})

it.each([
  { providerId: undefined, expectedId: 'APPLICATION_ID' },
  { providerId: '', expectedId: 'APPLICATION_ID' },
  { providerId: '123', expectedId: '123' },
])(
  'shows the Discord endpoint on the public URL with a separate API origin: %j',
  async ({ providerId, expectedId }) => {
    render(
      <IntegrationAdvanced
        integration={integration({
          integration_kind: 'discord_thread',
          provider_tenant_id: providerId,
        })}
      />,
      {
        api_url: 'https://public-api.example/api/v1',
        public_url: 'https://public-dashboard.example',
      },
    )
    act(() => {
      button('Advanced').click()
    })
    await waitForUI(() => {
      expect(container.textContent).toContain(
        `https://public-dashboard.example/api/integrations/discord/${expectedId}/interactions`,
      )
    })
  },
)

it.each(['github_pr', 'discord_thread'] as const)(
  'shows an unavailable public URL in advanced %s setup even with an API URL',
  async (kind) => {
    render(<IntegrationAdvanced integration={integration({ integration_kind: kind })} />, {
      api_url: 'https://public-api.example/api/v1',
    })
    act(() => {
      button('Advanced').click()
    })
    await waitForUI(() => {
      expect(container.textContent).toContain('Public URL unavailable')
    })
    expect(container.textContent).not.toContain('https://public-api.example')
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
