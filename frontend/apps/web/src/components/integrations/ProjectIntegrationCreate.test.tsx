/** @vitest-environment happy-dom */

import { listIntegrationDefinitionsQueryKey } from '@omnara/sdk/tanstack'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { ProjectIntegrationCreateSetup } from '@/routes/CreateProjectIntegrationPage'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId, integrationDefinition } from '@/test/fixtures'
import { renderProjectIntegration } from '@/test/project-integration-render'
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
  return renderProjectIntegration(root, api, content)
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
      (definition) => `/projects/${projectId}/integrations/new/${definition.integration_type}`,
    ),
  )
  expect([...container.querySelectorAll('h2')].map((heading) => heading.textContent)).toEqual([
    'Slack threads',
    'Discord threads',
    'GitHub PR review',
  ])
})

it.each([
  ['slack_thread', 'Use an existing Slack app'],
  ['github_pr', 'GitHub App owner'],
  ['discord_thread', 'Bot token'],
] as const)(
  'creates a %s integration through its connection form',
  async (integrationType, control) => {
    const api = fakeApi([
      {
        method: 'GET',
        path: `${projectPath}/integration-definitions`,
        respond: () => Response.json({ data: [integrationDefinition(integrationType)] }),
      },
    ])
    render(
      api,
      <ProjectIntegrationCreateSetup
        orgId={orgId}
        projectId={projectId}
        integrationType={integrationType}
      />,
    )
    await waitForUI(() => {
      expect(container.querySelector('[aria-label="Connection"]')?.textContent).toContain(control)
    })
    expect(container.querySelector('#integration-name')).not.toBeNull()
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
    <ProjectIntegrationCreateSetup
      orgId={orgId}
      projectId={projectId}
      integrationType="discord_thread"
    />,
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
