/** @vitest-environment happy-dom */

import { schemas } from '@omnara/sdk'
import { act } from 'react'
import { expect, it, vi } from 'vitest'

import { fakeApi, jsonResponse } from '@/test/fake-api'
import { projectIntegration } from '@/test/fixtures'
import { button, waitForUI } from '@/test/secret-editor'

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
  select,
  verified,
} from './github-setup-test-fixture'

it('does not run the connection callback after unmount while configure is pending', async () => {
  window.history.replaceState(null, '', `/?credentials_secret_ref=${secretId}`)
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
  click('Check installations')
  await waitForUI(() => {
    expect(document.querySelector('#github-installation')).not.toBeNull()
  })
  select('github-installation', '222')
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
    `/?github_setup_error=integration_setup_changed&credentials_secret_ref=${secretId}`,
  )
  const current = projectIntegration({
    integration_type: 'github_pr',
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
  expect(document.querySelector<HTMLSelectElement>('#saved-secret')?.value).toBe(secretId)
  expect(api.requestsTo('POST', inspectPath)).toHaveLength(0)
  click('Check installations')
  await waitForUI(() => {
    expect(document.querySelector('#github-installation')).not.toBeNull()
  })
  select('github-installation', '222')
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
    `/projects/${projectId}/integrations/${integration.id}?filter=kept&github_setup=credentials_saved&credentials_secret_ref=${secretId}#connection`,
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
              ...schemas.zConfigureProjectIntegrationRequest.parse(body),
              state: 'active',
              setup_revision: 2,
              provider_agent_display_name: 'Team reviewer',
            }),
    },
  ])
  const onConnected = vi.fn()
  render(
    api,
    <ConnectGitHubForm
      orgId={orgId}
      projectId={projectId}
      integration={integration}
      onConnected={onConnected}
    />,
  )
  expect(window.location.search).toBe('?filter=kept')
  expect(window.location.hash).toBe('#connection')
  expect(window.history.state).toEqual({ keep: true })
  expect(api.requestsTo('POST', inspectPath)).toHaveLength(0)
  click('Check installations')
  await waitForUI(() => {
    expect(container.textContent).toContain('No installations on this page')
  })
  expect(container.querySelector('a[href="' + verified.install_url + '"]')).not.toBeNull()
  expect(api.requestsTo('POST', integrationPath + '/setup')).toHaveLength(0)
  approved = true
  click('Refresh installations')
  await waitForUI(() => {
    expect(document.querySelector('#github-installation')).not.toBeNull()
  })
  expect(button('Connect integration').disabled).toBe(true)
  expect(
    document.querySelector<HTMLSelectElement>('#github-installation')?.labels[0]?.textContent,
  ).toBe('Installation')
  select('github-installation', '222')
  click('Connect integration')
  await waitForUI(() => {
    expect(container.textContent).toContain('Credential changed')
  })
  expect(document.querySelector<HTMLSelectElement>('#saved-secret')?.value).toBe(secretId)
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
    { credentials_secret_ref: secretId, page: 1 },
    { credentials_secret_ref: secretId, page: 1 },
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
    click('Check installations')
    await waitForUI(() => {
      expect(button('More installations')).toBeDefined()
    })
    expect(button('Connect integration').disabled).toBe(!hintOnFirstPage)
    expect(document.querySelector<HTMLSelectElement>('#github-installation')?.value).toBe(
      hintOnFirstPage ? '222' : '',
    )
    if (hintOnFirstPage) select('github-installation', '333')
    click('More installations')
    await waitForUI(() => {
      expect(button('Previous installations')).toBeDefined()
    })
    expect(document.querySelector<HTMLSelectElement>('#github-installation')?.value).toBe('')
    expect(button('Connect integration').disabled).toBe(true)
    click('Previous installations')
    await waitForUI(() => {
      expect(button('More installations')).toBeDefined()
    })
    expect(document.querySelector<HTMLSelectElement>('#github-installation')?.value).toBe('')
    expect(api.requestsTo('POST', integrationPath + '/setup')).toHaveLength(0)
  },
)

it('retries a failed installation check and discards installations from a replaced credential', async () => {
  window.history.replaceState(null, '', `/?credentials_secret_ref=${secretId}`)
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
    />,
  )
  click('Check installations')
  await waitForUI(() => {
    expect(container.querySelector('[role="alert"]')?.textContent).toBe('GitHub is unavailable')
    expect(button('Check installations').disabled).toBe(false)
  })
  expect(document.querySelector('#github-installation')).toBeNull()
  unavailable = false
  click('Check installations')
  await waitForUI(() => {
    expect(document.querySelector('#github-installation')).not.toBeNull()
  })
  expect(container.querySelector('[role="alert"]')).toBeNull()
  select('github-installation', '222')
  expect(button('Connect integration').disabled).toBe(false)
  await waitForUI(() => {
    expect(document.querySelector(`#saved-secret option[value="${replacement}"]`)).not.toBeNull()
  })
  select('saved-secret', replacement)
  expect(document.querySelector('#github-installation')).toBeNull()
  click('Check installations')
  await waitForUI(() => {
    expect(document.querySelector('#github-installation')).not.toBeNull()
  })
  expect(document.querySelector<HTMLSelectElement>('#github-installation')?.value).toBe('')
  expect(button('Connect integration').disabled).toBe(true)
  expect(api.requestsTo('POST', inspectPath).map((request) => request.body)).toEqual([
    { credentials_secret_ref: secretId, page: 1 },
    { credentials_secret_ref: secretId, page: 1 },
    { credentials_secret_ref: replacement, page: 1 },
  ])
  expect(api.requestsTo('POST', integrationPath + '/setup')).toHaveLength(0)
})
