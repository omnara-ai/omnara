/** @vitest-environment happy-dom */

import { listIntegrationDefinitionsQueryKey } from '@omnara/sdk/tanstack'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { IntegrationCreateSetup } from '@/routes/CreateIntegrationPage'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId, integrationDefinition } from '@/test/fixtures'
import { renderIntegration } from '@/test/integration-render'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, waitForUI } from '@/test/secret-editor'

import { IntegrationCatalog } from './IntegrationCatalog'

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
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function render(api: ReturnType<typeof fakeApi>, content: ReactNode) {
  return renderIntegration(root, api, content)
}

it('links every catalog entry by its exact integration type', async () => {
  const definitions = [
    integrationDefinition('slack_thread'),
    integrationDefinition('discord_thread'),
    integrationDefinition('github_pr'),
  ]
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/integration-definitions`,
      respond: () => Response.json({ data: definitions }),
    },
  ])
  render(api, <IntegrationCatalog orgId={orgId} projectId={projectId} />)
  await waitForUI(() => {
    expect(container.querySelectorAll('a')).toHaveLength(definitions.length)
  })
  expect([...container.querySelectorAll('a')].map((link) => link.getAttribute('href'))).toEqual(
    definitions.map(
      (definition) => `/projects/${projectId}/integrations/new/${definition.integration_kind}`,
    ),
  )
  expect([...container.querySelectorAll('h2')].map((heading) => heading.textContent)).toEqual([
    'Slack bot',
    'Discord bot',
    'GitHub PR review',
  ])
})

it.each([
  ['slack_thread', 'Enter app details', 'slack-bot'],
  ['github_pr', 'GitHub App owner', 'github-bot'],
  ['discord_thread', 'Bot token', 'discord-bot'],
] as const)(
  'creates a %s integration through its connection form',
  async (integrationKind, control, defaultName) => {
    const api = fakeApi([
      {
        method: 'GET',
        path: `${projectPath}/integration-definitions`,
        respond: () => Response.json({ data: [integrationDefinition(integrationKind)] }),
      },
    ])
    render(
      api,
      <IntegrationCreateSetup
        orgId={orgId}
        projectId={projectId}
        integrationKind={integrationKind}
      />,
    )
    await waitForUI(() => {
      expect(container.querySelector('[aria-label="Connection"]')?.textContent).toContain(control)
    })
    expect(container.querySelector<HTMLInputElement>('#integration-name')?.value).toBe(defaultName)
    expect(container.textContent).not.toContain('Name in Omnara')
    expect(container.textContent).not.toContain('A permanent name for this integration')
    await enter('Integration name', 'team-helper')
    expect(container.querySelector<HTMLInputElement>('#integration-name')?.value).toBe(
      'team-helper',
    )
    expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
  },
)

it('keeps creation fields mounted when refreshing integration definitions fails', async () => {
  let failing = false
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/integration-definitions`,
      respond: () =>
        failing
          ? jsonResponse({ code: 'internal_error', error: 'Temporary failure' }, 500)
          : Response.json({ data: [integrationDefinition('discord_thread')] }),
    },
  ])
  const { cache, client } = render(
    api,
    <IntegrationCreateSetup orgId={orgId} projectId={projectId} integrationKind="discord_thread" />,
  )
  await waitForUI(() => {
    expect(container.querySelector('#bot-token')).not.toBeNull()
  })
  await enter('Integration name', 'engineering')
  await enter('Bot token', 'unsaved-token')
  failing = true
  await act(async () => {
    await cache.invalidateQueries({
      queryKey: listIntegrationDefinitionsQueryKey({
        path: { orgID: orgId, projectID: projectId },
        client,
      }),
    })
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('Your setup is kept')
  })
  expect(container.querySelector<HTMLInputElement>('#integration-name')?.value).toBe('engineering')
  expect(container.querySelector<HTMLInputElement>('#bot-token')?.value).toBe('unsaved-token')
  failing = false
  act(() => {
    button('Retry refresh').click()
  })
  await waitForUI(() => {
    expect(container.textContent).not.toContain('Your setup is kept')
  })
  expect(container.querySelector<HTMLInputElement>('#bot-token')?.value).toBe('unsaved-token')
})
