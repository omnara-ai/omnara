/** @vitest-environment happy-dom */
import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, schemas } from '@omnara/sdk'
import { getProjectIntegrationQueryKey } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { type FakeApi, fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId, projectIntegration } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, waitForUI } from '@/test/secret-editor'

import { ConnectSlackForm } from './ConnectSlackForm'
import { ProjectIntegrationForm } from './ProjectIntegrationForm'
import { ProjectIntegrationSetup, ProjectIntegrationSetupForm } from './ProjectIntegrationSetup'
import { useProjectIntegrationDraft } from './useProjectIntegrationDraft'
import { useProjectIntegrationSetupState } from './useProjectIntegrationSetupState'

const orgId = fakeId('org'),
  projectId = fakeId('proj')
const path = `/api/v1/orgs/${orgId}/projects/${projectId}`
let root: Root, container: HTMLDivElement, cache: QueryClient, restore: () => void
beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
  cache = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  cache.clear()
  container.remove()
  restore()
  vi.restoreAllMocks()
})
function render(api: FakeApi, node: ReactNode) {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  const rerender = (next: ReactNode) => {
    act(() => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>{next}</QueryClientProvider>
        </OmnaraClientProvider>,
      )
    })
  }
  rerender(node)
  return { client, rerender }
}
async function submit() {
  await act(async () => {
    document
      .querySelector('form')
      ?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await Promise.resolve()
  })
}

function ManualSetupOwner({ visible, onSaved }: { visible: boolean; onSaved: () => void }) {
  const integration = projectIntegration({ integration_type: 'github_pr' })
  const draft = useProjectIntegrationDraft(orgId, projectId, 'github_pr', integration)
  const state = useProjectIntegrationSetupState(integration, { credentialSecretId: fakeId('sec') })
  return (
    <>
      <output>{state.busy ? 'busy' : 'idle'}</output>
      {visible && (
        <ProjectIntegrationSetupForm
          orgId={orgId}
          projectId={projectId}
          integration={integration}
          integrationType="github_pr"
          draft={draft}
          state={state}
          onSaved={onSaved}
        />
      )}
    </>
  )
}

it.each([200, 500])(
  'releases parent setup state after the manual form unmounts (status=%s)',
  async (status) => {
    const integration = projectIntegration({ integration_type: 'github_pr' })
    const setupPath = `${path}/integrations/${integration.id}/setup`
    let release!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => {
      release = resolve
    })
    const api = fakeApi([
      {
        method: 'GET',
        path: path + '/secrets',
        respond: () => Response.json({ data: [], next_cursor: null }),
      },
      {
        method: 'GET',
        path: `${path}/integrations/${integration.id}`,
        respond: () => Response.json(integration),
      },
      { method: 'POST', path: setupPath, respond: () => pending },
    ])
    const onSaved = vi.fn()
    const { rerender } = render(api, <ManualSetupOwner visible onSaved={onSaved} />)
    await enter('GitHub App ID', '111')
    await enter('Installation ID', '222')
    await submit()
    await waitForUI(() => {
      expect(api.requestsTo('POST', setupPath)).toHaveLength(1)
    })
    rerender(<ManualSetupOwner visible={false} onSaved={onSaved} />)
    expect(container.querySelector('form')).toBeNull()
    expect(container.querySelector('output')?.textContent).toBe('busy')
    act(() => {
      release(
        status === 200
          ? Response.json({ ...integration, state: 'active', setup_revision: 2 })
          : jsonResponse({ code: 'unavailable', error: 'Try again' }, status),
      )
    })
    await waitForUI(() => {
      expect(container.querySelector('output')?.textContent).toBe('idle')
    })
    expect(onSaved).not.toHaveBeenCalled()
    rerender(<ManualSetupOwner visible onSaved={onSaved} />)
    expect(button('Connect integration').disabled).toBe(false)
  },
)

it.each(['Integration name already exists', 'project integrations limit of 64 reached'])(
  'preserves creation error %s and credentials through retry',
  async (message) => {
    let attempts = 0
    const integration = projectIntegration({
      integration_type: 'discord_thread',
      name: 'engineering-2',
    })
    const api = fakeApi([
      {
        method: 'POST',
        path: path + '/integrations',
        respond: () =>
          ++attempts === 1
            ? jsonResponse({ code: 'conflict', error: message }, 409)
            : Response.json(integration, { status: 201 }),
      },
      {
        method: 'GET',
        path: path + '/integrations/' + integration.id,
        respond: () => Response.json(integration),
      },
      {
        method: 'POST',
        path: `/api/v1/orgs/${orgId}/secrets`,
        respond: () => jsonResponse({ code: 'invalid_request', error: 'Check credentials' }, 400),
      },
    ])
    render(
      api,
      <ProjectIntegrationSetup
        orgId={orgId}
        projectId={projectId}
        integrationType="discord_thread"
        onSaved={vi.fn()}
      />,
    )
    await enter('Discord Application ID', '111')
    await enter('Bot token', 'token')
    await enter('Public key', 'ab'.repeat(32))
    await enter('Integration name', 'with space')
    await submit()
    expect(container.textContent).toContain('1–32')
    expect(api.requests).toHaveLength(0)
    await enter('Integration name', 'engineering')
    await submit()
    await waitForUI(() => {
      expect(container.textContent).toContain(message)
      expect(container.textContent).toContain('open it from Integrations to continue')
      expect(container.querySelector<HTMLInputElement>('#integration-name')?.readOnly).toBe(false)
    })
    await enter('Integration name', 'engineering-2')
    await submit()
    await waitForUI(() => {
      expect(container.textContent).toContain('Check credentials')
    })
    expect(api.requestsTo('POST', path + '/integrations').at(-1)?.body).toEqual({
      name: 'engineering-2',
      integration_type: 'discord_thread',
      settings: {},
    })
    expect(container.querySelector<HTMLInputElement>('#provider-tenant')?.value).toBe('111')
    expect(container.querySelector<HTMLInputElement>('#bot-token')?.value).toBe('token')
    expect(container.querySelector<HTMLInputElement>('#integration-name')?.readOnly).toBe(true)
    expect(
      [...container.querySelectorAll('a')]
        .find((link) => link.textContent === 'resume setup from its page')
        ?.getAttribute('href'),
    ).toBe(`/projects/${integration.project_id}/integrations/${integration.id}`)
  },
)

it.each([
  ['github_pr', false],
  ['discord_thread', false],
  ['github_pr', true],
  ['discord_thread', true],
] as const)(
  'retries %s setup (creating=%s) using the saved integration and credential',
  async (integrationType, creating) => {
    const integration = projectIntegration({ integration_type: integrationType })
    let attempts = 0
    const api = fakeApi([
      {
        method: 'POST',
        path: path + '/integrations',
        respond: () => Response.json(integration, { status: 201 }),
      },
      {
        method: 'POST',
        path: `/api/v1/orgs/${orgId}/secrets`,
        respond: () =>
          Response.json(
            {
              id: fakeId('sec'),
              org_id: orgId,
              name: 'Credentials',
              owner: { kind: 'project', project_id: projectId },
              kind: integrationType === 'github_pr' ? 'github_app_credentials' : 'generic',
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
        path: path + '/integrations/' + integration.id + '/setup',
        respond: ({ body }) => {
          const setup = schemas.zConfigureProjectIntegrationRequest.parse(body)
          return ++attempts === 1
            ? jsonResponse({ code: 'conflict', error: 'Verification failed; try again' }, 409)
            : Response.json({
                ...integration,
                ...setup,
                provider_account_ref: '222',
                state: 'active',
                setup_revision: 2,
              })
        },
      },
      {
        method: 'GET',
        path: path + '/integrations/' + integration.id,
        respond: () => Response.json(integration),
      },
    ])
    const onSaved = vi.fn()
    render(
      api,
      <ProjectIntegrationSetup
        orgId={orgId}
        projectId={projectId}
        integration={creating ? undefined : integration}
        integrationType={integrationType}
        onSaved={onSaved}
        onCancel={vi.fn()}
      />,
    )
    await enter(integrationType === 'github_pr' ? 'GitHub App ID' : 'Discord Application ID', '111')
    if (integrationType === 'github_pr') await enter('Installation ID', '222')
    if (integrationType === 'github_pr') {
      await enter('RSA private key (PEM)', 'test-key')
      await enter('Webhook secret', 'signature')
    } else {
      await enter('Bot token', 'token')
      await enter('Public key', 'ab'.repeat(32))
    }
    await submit()
    await waitForUI(() => {
      expect(container.textContent).toContain('Verification failed; try again')
    })
    expect(container.textContent).toContain('Credentials saved')
    await submit()
    await waitForUI(() => {
      expect(onSaved).toHaveBeenCalledWith(
        expect.objectContaining({ id: integration.id, state: 'active' }),
      )
    })
    expect(api.requestsTo('POST', `/api/v1/orgs/${orgId}/secrets`)).toHaveLength(1)
    expect(
      api.requestsTo('POST', path + '/integrations/' + integration.id + '/setup'),
    ).toHaveLength(2)
    expect(api.requestsTo('POST', path + '/integrations')).toHaveLength(creating ? 1 : 0)
    expect(
      api.requestsTo('POST', path + '/integrations/' + integration.id + '/setup').at(-1)?.body,
    ).toMatchObject({
      expected_setup_revision: 1,
      credential_secret_id: fakeId('sec'),
      provider_tenant_id: '111',
    })
    if (integrationType === 'discord_thread') {
      expect(
        api.requestsTo('POST', path + '/integrations/' + integration.id + '/setup').at(-1)?.body,
      ).not.toHaveProperty('provider_account_ref')
      expect(
        api.requestsTo('POST', path + '/integrations/' + integration.id + '/setup').at(-1)?.body,
      ).toMatchObject({
        provider_config: { public_key: 'ab'.repeat(32) },
      })
    } else {
      expect(
        api.requestsTo('POST', path + '/integrations/' + integration.id + '/setup').at(-1)?.body,
      ).toHaveProperty('provider_account_ref', '222')
    }
  },
)

it('keeps the displayed GitHub application and endpoint aligned when another tab connects the integration', async () => {
  const integration = projectIntegration({ integration_type: 'github_pr' })
  const props = { orgId, projectId, integrationType: 'github_pr' as const, onSaved: vi.fn() }
  const { rerender } = render(
    fakeApi([]),
    <ProjectIntegrationSetup {...props} integration={integration} />,
  )
  const value = (id: string) => container.querySelector<HTMLInputElement>(`#${id}`)?.value
  expect(value('provider-endpoint')).toBe('')
  expect(button('Copy').disabled).toBe(true)
  await enter('GitHub App ID', '111')
  expect(value('provider-endpoint')).toBe('https://omnara.test/api/integrations/github/111/events')
  expect(button('Copy').disabled).toBe(false)
  rerender(
    <ProjectIntegrationSetup
      {...props}
      integration={{
        ...integration,
        state: 'active',
        provider_tenant_id: '333',
        provider_account_ref: '444',
      }}
    />,
  )
  expect(value('provider-tenant')).toBe('333')
  expect(value('provider-endpoint')).toBe('https://omnara.test/api/integrations/github/333/events')
})

it('blocks a stale edit until explicitly reloaded', async () => {
  const integration = projectIntegration({ integration_type: 'github_pr' })
  const next = { ...integration, updated_at: '2026-09-20T00:00:00Z' }
  const api = fakeApi([])
  const props = { orgId, projectId, integrationType: 'github_pr' as const, onSaved: vi.fn() }
  const { rerender } = render(api, <ProjectIntegrationForm {...props} integration={integration} />)
  rerender(<ProjectIntegrationForm {...props} integration={next} />)
  expect(container.textContent).toContain('Integration changed. Reload settings')
  await submit()
  expect(api.requests).toHaveLength(0)
  act(() => {
    button('Reload settings').click()
  })
  expect(container.textContent).not.toContain('Integration changed.')
})

it('keeps cancellation disabled during saving and ignores completion after unmount', async () => {
  const integration = projectIntegration()
  let release!: (response: Response) => void
  const api = fakeApi([
    {
      method: 'GET',
      path: path + '/agent-profiles',
      respond: () => Response.json({ data: [], next_cursor: null }),
    },
    {
      method: 'PUT',
      path: path + '/integrations/' + integration.id,
      respond: () =>
        new Promise((resolve) => {
          release = resolve
        }),
    },
  ])
  const onSaved = vi.fn(),
    onCancel = vi.fn()
  const { rerender } = render(
    api,
    <ProjectIntegrationForm
      orgId={orgId}
      projectId={projectId}
      integrationType="slack_thread"
      integration={integration}
      onSaved={onSaved}
      onCancel={onCancel}
    />,
  )
  await submit()
  await waitForUI(() => {
    expect(api.requestsTo('PUT', path + '/integrations/' + integration.id)).toHaveLength(1)
  })
  await waitForUI(() => {
    expect(button('Cancel').disabled).toBe(true)
  })
  act(() => {
    button('Cancel').click()
  })
  expect(onCancel).not.toHaveBeenCalled()
  rerender(null)
  await act(async () => {
    release(Response.json(projectIntegration(), { status: 201 }))
    await Promise.resolve()
  })
  expect(onSaved).not.toHaveBeenCalled()
})

it.each([false, true])(
  'retries Slack authorization with the saved integration (creating=%s)',
  async (creating) => {
    const integration = projectIntegration(
      creating ? {} : { provider_tenant_id: 'T123', provider_account_ref: 'A123' },
    )
    const api = fakeApi([
      {
        method: 'POST',
        path: path + '/integrations',
        respond: () => Response.json(integration, { status: 201 }),
      },
      {
        method: 'GET',
        path: path + '/integrations/' + integration.id,
        respond: () => Response.json(integration),
      },
      {
        method: 'POST',
        path: path + '/integrations/' + integration.id + '/oauth/setup',
        respond: () => jsonResponse({ code: 'conflict', error: 'Try authorization again' }, 409),
      },
    ])
    render(
      api,
      <ConnectSlackForm
        integration={creating ? undefined : integration}
        orgId={orgId}
        projectId={projectId}
      />,
    )
    if (creating)
      act(() => {
        container.querySelector<HTMLInputElement>('input[type="checkbox"]')?.click()
      })
    await enter('Client ID', 'client')
    await enter('Client secret', 'secret')
    await enter('Signing secret', 'signature')
    await submit()
    await waitForUI(() => {
      expect(document.body.textContent).toContain('Try authorization again')
    })
    expect(
      api.requestsTo('POST', path + '/integrations/' + integration.id + '/oauth/setup')[0]?.body,
    ).toEqual({
      client_id: 'client',
      client_secret: 'secret',
      signing_secret: 'signature',
      return_to: `/projects/${projectId}/integrations/${integration.id}`,
    })
    await submit()
    expect(api.requestsTo('POST', path + '/integrations')).toHaveLength(creating ? 1 : 0)
    expect(
      api.requestsTo('POST', path + '/integrations/' + integration.id + '/oauth/setup'),
    ).toHaveLength(2)
  },
)

it('completes OAuth only for the exact integration flow, not a previous active flow', async () => {
  const integration = projectIntegration({
    provider_tenant_id: 'T123',
    provider_account_ref: 'A123',
  })
  const flowId = fakeId('ioaf')
  const setup = {
    integration_id: integration.id,
    setup_revision: integration.setup_revision,
    provider: 'slack',
    flow_id: flowId,
    oauth_url: 'https://slack.com/oauth/v2/authorize',
    redirect_uri: 'https://omnara.test/callback',
    events_url: 'https://omnara.test/events',
    actions_url: 'https://omnara.test/actions',
    expires_at: new Date(Date.now() + 600_000).toISOString(),
  }
  const api = fakeApi([
    {
      method: 'POST',
      path: path + '/integrations/' + integration.id + '/oauth/setup',
      respond: () => Response.json(setup, { status: 201 }),
    },
    {
      method: 'GET',
      path: path + '/integrations/' + integration.id,
      respond: () =>
        Response.json({
          ...integration,
          state: 'active',
          setup_revision: integration.setup_revision,
          last_oauth_flow_id: `ioaf_${'b'.repeat(26)}`,
        }),
    },
  ])
  const onConnected = vi.fn()
  const { client } = render(
    api,
    <ConnectSlackForm
      integration={integration}
      orgId={orgId}
      projectId={projectId}
      onConnected={onConnected}
    />,
  )
  await enter('Client ID', 'client')
  await enter('Client secret', 'secret')
  await enter('Signing secret', 'signature')
  await submit()
  await waitForUI(() => {
    expect(api.requestsTo('GET', path + '/integrations/' + integration.id)).toHaveLength(1)
  })
  expect(document.querySelector('a')?.getAttribute('href')).toBe(setup.oauth_url)
  expect(onConnected).not.toHaveBeenCalled()
  const key = getProjectIntegrationQueryKey({
    path: { orgID: orgId, projectID: projectId, integrationID: integration.id },
    client,
  })
  act(() => {
    cache.setQueryData(key, { ...integration, state: 'disconnected', last_oauth_flow_id: flowId })
  })
  expect(onConnected).not.toHaveBeenCalled()
  act(() => {
    cache.setQueryData(key, {
      ...integration,
      state: 'active',
      setup_revision: integration.setup_revision + 1,
      last_oauth_flow_id: flowId,
    })
  })
  await waitForUI(() => {
    expect(onConnected).toHaveBeenCalledOnce()
  })
})

it('keeps the reconnect credential selected when its fallback option is replaced by fetched secrets', async () => {
  const secretID = fakeId('sec')
  const integration = projectIntegration({
    integration_type: 'github_pr',
    credential_secret_id: secretID,
    provider_tenant_id: '111',
    provider_account_ref: '222',
  })
  let resolveSecrets: (response: Response) => void = () => {
    throw new Error('Secret response is not ready')
  }
  const pending = new Promise<Response>((resolve) => {
    resolveSecrets = resolve
  })
  const api = fakeApi([
    { method: 'GET', path: path + '/secrets', respond: () => pending },
    {
      method: 'POST',
      path: path + '/integrations/' + integration.id + '/setup',
      respond: () => Response.json({ ...integration, state: 'active', setup_revision: 2 }),
    },
  ])
  const onSaved = vi.fn()
  render(
    api,
    <ProjectIntegrationSetup
      orgId={orgId}
      projectId={projectId}
      integration={integration}
      integrationType="github_pr"
      onSaved={onSaved}
      onCancel={vi.fn()}
    />,
  )
  const selected = () => container.querySelector<HTMLSelectElement>('#saved-secret')?.value
  expect(container.textContent).toContain('Current credential')
  expect(selected()).toBe(secretID)
  await act(async () => {
    resolveSecrets(
      Response.json({
        data: [
          {
            secret: {
              id: secretID,
              org_id: orgId,
              name: 'Saved GitHub credential',
              kind: 'github_app_credentials',
              owner: { kind: 'project', project_id: projectId },
              management_kind: 'tenant',
              metadata: {},
              current_version_number: 1,
              payload_keys: [],
              created_at: integration.created_at,
              updated_at: integration.updated_at,
            },
            availability: { source: 'direct', project_id: projectId },
          },
        ],
        next_cursor: null,
      }),
    )
    await pending
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('Saved GitHub credential')
  })
  expect(selected()).toBe(secretID)
  await submit()
  await waitForUI(() => {
    expect(onSaved).toHaveBeenCalled()
  })
  expect(
    api.requestsTo('POST', path + '/integrations/' + integration.id + '/setup')[0]?.body,
  ).toMatchObject({
    credential_secret_id: secretID,
  })
})
