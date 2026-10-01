/** @vitest-environment happy-dom */

import { schemas } from '@omnara/sdk'
import { act, StrictMode } from 'react'
import { expect, it, vi } from 'vitest'

import { fakeApi, jsonResponse } from '@/test/fake-api'
import { integration as integrationFixture } from '@/test/fixtures'
import { button, choose, waitForUI } from '@/test/secret-editor'

import { ConnectGitHubForm } from './ConnectGitHubForm'
import {
  click,
  container,
  credential,
  credentialPage,
  inspectPath,
  integration,
  integrationPath,
  orgId,
  projectId,
  projectPath,
  reads,
  render,
  secretId,
  verified,
} from './github-setup-test-fixture'

it('does not run the connection callback after unmount while configure is pending', async () => {
  window.history.replaceState(null, '', `/?credential_secret_id=${secretId}`)
  let release!: (response: Response) => void
  const pending = new Promise<Response>((resolve) => {
    release = resolve
  })
  const api = fakeApi([
    ...reads,
    { method: 'POST', path: inspectPath, respond: () => Response.json(verified) },
    { method: 'POST', path: integrationPath + '/setup', respond: () => pending },
  ])
  const onConnected = vi.fn()
  const { cache, rerender } = render(
    api,
    <ConnectGitHubForm
      orgId={orgId}
      projectId={projectId}
      integration={integration}
      onConnected={onConnected}
    />,
  )
  click('Check GitHub access')
  await waitForUI(() => {
    expect(container.textContent).toContain('GitHub account: engineering')
  })
  click('Connect integration')
  await waitForUI(() => {
    expect(api.requestsTo('POST', integrationPath + '/setup')).toHaveLength(1)
  })
  rerender(<p>Another page</p>)
  act(() => {
    release(Response.json({ ...integration, state: 'active', setup_revision: 2 }))
  })
  await waitForUI(() => {
    expect(cache.isMutating()).toBe(0)
  })
  expect(onConnected).not.toHaveBeenCalled()
  expect(container.textContent).toBe('Another page')
})

it('keeps a saved callback credential after setup changes and confirms against the current revision', async () => {
  window.history.replaceState(
    null,
    '',
    `/?github_setup_error=integration_setup_changed&credential_secret_id=${secretId}`,
  )
  const current = integrationFixture({
    integration_kind: 'github_pr',
    state: 'active',
    setup_revision: 3,
    provider_tenant_id: '111',
    provider_account_ref: '222',
  })
  const api = fakeApi([
    ...reads,
    { method: 'POST', path: inspectPath, respond: () => Response.json(verified) },
    { method: 'POST', path: integrationPath + '/setup', respond: () => Response.json(current) },
  ])
  const onConnected = vi.fn()
  render(
    api,
    <ConnectGitHubForm
      orgId={orgId}
      projectId={projectId}
      integration={current}
      onConnected={onConnected}
    />,
  )
  expect(container.querySelector('[role="alert"]')?.textContent).toContain(
    'Your credential was saved',
  )
  expect(document.getElementById('saved-secret')?.textContent).toBe(secretId)
  expect(api.requestsTo('POST', inspectPath)).toHaveLength(0)
  click('Check GitHub access')
  await waitForUI(() => {
    expect(container.textContent).toContain('GitHub account: engineering')
  })
  click('Connect integration')
  await waitForUI(() => {
    expect(onConnected).toHaveBeenCalledOnce()
  })
  expect(api.requestsTo('POST', integrationPath + '/setup')[0]?.body).toMatchObject({
    expected_setup_revision: 3,
    credential_secret_id: secretId,
  })
  expect(api.requestsTo('POST', integrationPath + '/github-setup')).toHaveLength(0)
})

it('resumes a saved credential after approval and connects only on explicit confirmation', async () => {
  window.history.replaceState(
    { keep: true },
    '',
    `/projects/${projectId}/integrations/${integration.id}?filter=kept&github_setup=credentials_saved&credential_secret_id=${secretId}#connection`,
  )
  let approved = false,
    connects = 0
  const api = fakeApi([
    ...reads,
    {
      method: 'POST',
      path: inspectPath,
      respond: () =>
        Response.json({ ...verified, installations: approved ? verified.installations : [] }),
    },
    {
      method: 'POST',
      path: integrationPath + '/setup',
      respond: ({ body }) =>
        ++connects === 1
          ? jsonResponse(
              { code: 'conflict', error: 'Credential changed. Retry verification.' },
              409,
            )
          : Response.json({
              ...integration,
              ...schemas.zConfigureIntegrationRequest.parse(body),
              state: 'active',
              setup_revision: 2,
              provider_agent_display_name: 'Team reviewer',
            }),
    },
  ])
  const onConnected = vi.fn()
  render(
    api,
    <StrictMode>
      <ConnectGitHubForm
        orgId={orgId}
        projectId={projectId}
        integration={integration}
        onConnected={onConnected}
        footerAction={<button type="button">Delete integration</button>}
      />
    </StrictMode>,
  )
  expect(window.location.search).toBe('?filter=kept')
  expect(window.location.hash).toBe('#connection')
  expect(window.history.state).toEqual({ keep: true })
  await waitForUI(() => {
    expect(container.textContent).toContain('The App can’t access any repositories yet')
  })
  expect(api.requestsTo('POST', inspectPath)).toHaveLength(1)
  expect(document.getElementById('saved-secret')).toBeNull()
  expect(container.textContent).not.toContain('Delete integration')
  expect(container.textContent).not.toContain('Enter App details')
  expect(
    [...container.querySelectorAll('a')]
      .find((link) => link.textContent === 'Choose repositories on GitHub')
      ?.getAttribute('href'),
  ).toBe(verified.install_url)
  expect(api.requestsTo('POST', integrationPath + '/setup')).toHaveLength(0)
  approved = true
  click('I’ve granted access')
  await waitForUI(() => {
    expect(container.textContent).toContain('GitHub account: engineering')
  })
  expect(document.querySelector('#github-installation')).toBeNull()
  expect(api.requestsTo('POST', integrationPath + '/setup')).toHaveLength(0)
  click('Connect integration')
  await waitForUI(() => {
    expect(container.textContent).toContain('Credential changed')
  })
  expect(container.textContent).toContain('GitHub account: engineering')
  click('Connect integration')
  await waitForUI(() => {
    expect(onConnected).toHaveBeenCalledOnce()
  })
  expect(api.requestsTo('POST', integrationPath + '/setup')[1]?.body).toEqual({
    expected_setup_revision: 1,
    provider_tenant_id: '111',
    provider_account_ref: '222',
    credential_secret_id: secretId,
  })
  expect(api.requestsTo('POST', inspectPath).map((request) => request.body)).toEqual([
    { credential_secret_id: secretId, page: 1 },
    { credential_secret_id: secretId, page: 1 },
  ])
  expect(api.requestsTo('POST', integrationPath + '/github-setup')).toHaveLength(0)
})

it.each([true, false])(
  'uses an installation hint only on the first verified page (present=%s)',
  async (hintOnFirstPage) => {
    window.history.replaceState(
      null,
      '',
      `/projects/${projectId}/integrations/${integration.id}?state=${secretId}&installation_id=222&setup_action=install`,
    )
    const api = fakeApi([
      ...reads,
      {
        method: 'POST',
        path: inspectPath,
        respond: ({ body }) => {
          const request = schemas.zInspectGitHubInstallationsRequest.parse(body)
          return Response.json(
            request.page === 2
              ? verified
              : {
                  ...verified,
                  installations: hintOnFirstPage
                    ? [
                        ...verified.installations,
                        { ...verified.installations[0], id: '333', account: 'another-account' },
                      ]
                    : [{ ...verified.installations[0], id: '333', account: 'another-account' }],
                  next_page: 2,
                },
          )
        },
      },
    ])
    render(
      api,
      <ConnectGitHubForm
        orgId={orgId}
        projectId={projectId}
        integration={integration}
        onConnected={vi.fn()}
      />,
    )
    await waitForUI(() => {
      expect(button('More accounts')).toBeDefined()
    })
    expect(api.requestsTo('POST', inspectPath)).toHaveLength(1)
    expect(button('Connect integration').disabled).toBe(!hintOnFirstPage)
    expect(
      document.querySelector<HTMLButtonElement>('#github-installation')?.labels[0]?.textContent,
    ).toBe('GitHub account')
    expect(document.getElementById('github-installation')?.textContent).toBe(
      hintOnFirstPage ? 'engineering' : 'Choose an account',
    )
    if (hintOnFirstPage) await choose('GitHub account', 'another-account')
    click('More accounts')
    await waitForUI(() => {
      expect(button('Previous accounts')).toBeDefined()
    })
    expect(document.getElementById('github-installation')?.textContent).toBe('Choose an account')
    expect(button('Connect integration').disabled).toBe(true)
    click('Previous accounts')
    await waitForUI(() => {
      expect(button('More accounts')).toBeDefined()
    })
    expect(document.getElementById('github-installation')?.textContent).toBe('Choose an account')
    expect(api.requestsTo('POST', integrationPath + '/setup')).toHaveLength(0)
  },
)

it('retries a failed installation check and discards installations from a replaced credential', async () => {
  window.history.replaceState(null, '', `/?credential_secret_id=${secretId}`)
  const replacement = `sec_${'b'.repeat(26)}`
  let unavailable = true
  const api = fakeApi([
    {
      method: 'GET',
      path: projectPath + '/secrets',
      respond: () =>
        credentialPage(credential(secretId, 'First'), credential(replacement, 'Second')),
    },
    { method: 'GET', path: integrationPath, respond: () => Response.json(integration) },
    {
      method: 'POST',
      path: inspectPath,
      respond: ({ body }) =>
        unavailable
          ? jsonResponse({ code: 'unavailable', error: 'GitHub is unavailable' }, 503)
          : Response.json(
              schemas.zInspectGitHubInstallationsRequest.parse(body).credential_secret_id ===
                replacement
                ? {
                    ...verified,
                    installations: [
                      ...verified.installations,
                      { ...verified.installations[0], id: '333', account: 'another-account' },
                    ],
                  }
                : verified,
            ),
    },
  ])
  render(
    api,
    <ConnectGitHubForm
      orgId={orgId}
      projectId={projectId}
      integration={integration}
      onConnected={vi.fn()}
    />,
  )
  click('Check GitHub access')
  await waitForUI(() => {
    expect(container.querySelector('[role="alert"]')?.textContent).toBe('GitHub is unavailable')
    expect(button('Check GitHub access').disabled).toBe(false)
  })
  expect(container.textContent).not.toContain('GitHub App:')
  unavailable = false
  click('Check GitHub access')
  await waitForUI(() => {
    expect(container.textContent).toContain('GitHub account: engineering')
  })
  expect(container.querySelector('[role="alert"]')).toBeNull()
  expect(button('Connect integration').disabled).toBe(false)
  await choose('Saved credential', 'Second')
  expect(container.textContent).not.toContain('GitHub App:')
  click('Check GitHub access')
  await waitForUI(() => {
    expect(document.querySelector('#github-installation')).not.toBeNull()
  })
  expect(document.getElementById('github-installation')?.textContent).toBe('Choose an account')
  expect(button('Connect integration').disabled).toBe(true)
  expect(api.requestsTo('POST', inspectPath).map((request) => request.body)).toEqual([
    { credential_secret_id: secretId, page: 1 },
    { credential_secret_id: secretId, page: 1 },
    { credential_secret_id: replacement, page: 1 },
  ])
  expect(api.requestsTo('POST', integrationPath + '/setup')).toHaveLength(0)
})

it('falls back to saved-credential setup when the returned access check fails', async () => {
  window.history.replaceState(
    null,
    '',
    `/?github_setup=credentials_saved&credential_secret_id=${secretId}`,
  )
  let unavailable = true
  const api = fakeApi([
    ...reads,
    {
      method: 'POST',
      path: inspectPath,
      respond: () =>
        unavailable
          ? jsonResponse({ code: 'unavailable', error: 'GitHub is unavailable' }, 503)
          : Response.json(verified),
    },
  ])
  render(
    api,
    <ConnectGitHubForm
      orgId={orgId}
      projectId={projectId}
      integration={integration}
      onConnected={vi.fn()}
      footerAction={<button type="button">Delete integration</button>}
    />,
  )
  expect(container.textContent).toContain('Checking GitHub access')
  await waitForUI(() => {
    expect(container.querySelector('[role="alert"]')?.textContent).toBe('GitHub is unavailable')
    expect(button('Check GitHub access').disabled).toBe(false)
  })
  expect(api.requestsTo('POST', inspectPath)).toHaveLength(1)
  expect(document.getElementById('saved-secret')?.textContent).toBe(secretId)
  expect(button('Enter App details')).toBeDefined()
  expect(button('Delete integration')).toBeDefined()
  unavailable = false
  click('Check GitHub access')
  await waitForUI(() => {
    expect(container.textContent).toContain('GitHub account: engineering')
  })
  expect(api.requestsTo('POST', inspectPath)).toHaveLength(2)
  expect(api.requestsTo('POST', integrationPath + '/setup')).toHaveLength(0)
})
