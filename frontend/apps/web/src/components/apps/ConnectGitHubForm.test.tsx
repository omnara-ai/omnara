/** @vitest-environment happy-dom */

import { schemas } from '@omnara/sdk'
import { act } from 'react'
import { expect, it, vi } from 'vitest'

import { fakeApi, jsonResponse } from '@/test/fake-api'
import { button, enter, field, waitForUI } from '@/test/secret-editor'

import { ConnectGitHubForm } from './ConnectGitHubForm'
import {
  app,
  appPath,
  click,
  container,
  credential,
  credentialPage,
  inspectPath,
  orgId,
  projectId,
  projectPath,
  reads,
  render,
  secretId,
  select,
  verified,
} from './github-setup-test-fixture'

it.each([
  { step: 'app creation', status: 201 },
  { step: 'app creation', status: 500 },
  { step: 'registration', status: 201 },
  { step: 'registration', status: 500 },
])(
  'does not continue GitHub registration after unmount during $step (status=$status)',
  async ({ step, status }) => {
    let release!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => {
      release = resolve
    })
    const setup = {
      app_id: app.id,
      setup_revision: 1,
      registration_url: 'https://github.com/settings/apps/new?state=sealed',
      manifest: { name: 'reviewer', public: false },
      expires_at: '2026-09-22T01:00:00Z',
    }
    const createPath = projectPath + '/apps',
      startPath = appPath + '/github-setup'
    const api = fakeApi([
      ...reads,
      {
        method: 'POST',
        path: createPath,
        respond: () => (step === 'app creation' ? pending : Response.json(app, { status: 201 })),
      },
      {
        method: 'POST',
        path: startPath,
        respond: () => (step === 'registration' ? pending : Response.json(setup, { status: 201 })),
      },
    ])
    const post = vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(() => undefined)
    const onConnected = vi.fn()
    const { cache, rerender } = render(
      api,
      <ConnectGitHubForm orgId={orgId} projectId={projectId} onConnected={onConnected} />,
    )
    click('Continue to GitHub')
    await waitForUI(() => {
      expect(api.requestsTo('POST', step === 'app creation' ? createPath : startPath)).toHaveLength(
        1,
      )
    })
    rerender(<p>Another page</p>)
    window.history.replaceState(null, '', '/after-leaving')
    act(() => {
      release(
        status === 201
          ? Response.json(step === 'app creation' ? app : setup, { status })
          : jsonResponse({ code: 'unavailable', error: 'Try again' }, status),
      )
    })
    await waitForUI(() => {
      expect(cache.isMutating()).toBe(0)
    })
    expect(api.requestsTo('POST', createPath)).toHaveLength(1)
    expect(api.requestsTo('POST', startPath)).toHaveLength(step === 'app creation' ? 0 : 1)
    expect(post).not.toHaveBeenCalled()
    expect(onConnected).not.toHaveBeenCalled()
    expect(document.querySelector('form')).toBeNull()
    expect(container.textContent).toBe('Another page')
    expect(window.location.pathname).toBe('/after-leaving')
  },
)

it('reports a mismatched registration app and releases the busy state without posting', async () => {
  const api = fakeApi([
    ...reads,
    {
      method: 'POST',
      path: appPath + '/github-setup',
      respond: () =>
        Response.json(
          {
            app_id: `app_${'b'.repeat(26)}`,
            setup_revision: 1,
            registration_url: 'https://github.com/settings/apps/new?state=sealed',
            manifest: { name: 'reviewer', public: false },
            expires_at: '2026-09-22T01:00:00Z',
          },
          { status: 201 },
        ),
    },
  ])
  const post = vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(() => undefined)
  render(
    api,
    <ConnectGitHubForm orgId={orgId} projectId={projectId} app={app} onConnected={vi.fn()} />,
  )
  click('Continue to GitHub')
  await waitForUI(() => {
    expect(container.textContent).toContain('Registration returned a different app.')
    expect(button('Continue to GitHub').disabled).toBe(false)
  })
  expect(post).not.toHaveBeenCalled()
})

it('explains an app creation conflict before GitHub registration', async () => {
  const api = fakeApi([
    ...reads,
    {
      method: 'POST',
      path: projectPath + '/apps',
      respond: () => jsonResponse({ code: 'conflict', error: 'App name already exists' }, 409),
    },
  ])
  const post = vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(() => undefined)
  render(api, <ConnectGitHubForm orgId={orgId} projectId={projectId} onConnected={vi.fn()} />)
  click('Continue to GitHub')
  await waitForUI(() => {
    expect(container.querySelector('[role="alert"]')?.textContent).toBe(
      'App name already exists. If you started setup earlier, open it from Apps to continue.',
    )
  })
  expect(api.requestsTo('POST', appPath + '/github-setup')).toHaveLength(0)
  expect(post).not.toHaveBeenCalled()
})

it.each([false, true])(
  'uses the returned credential in manual setup (inspected=%s)',
  async (inspected) => {
    window.history.replaceState(null, '', `/?credentials_secret_ref=${secretId}`)
    const api = fakeApi([
      ...reads,
      { method: 'POST', path: inspectPath, respond: () => Response.json(verified) },
      {
        method: 'POST',
        path: appPath + '/setup',
        respond: ({ body }) =>
          Response.json({
            ...app,
            ...schemas.zConfigureProjectAppRequest.parse(body),
            state: 'active',
          }),
      },
    ])
    const onConnected = vi.fn()
    render(
      api,
      <ConnectGitHubForm orgId={orgId} projectId={projectId} app={app} onConnected={onConnected} />,
    )
    if (inspected) {
      click('Check installations')
      await waitForUI(() => {
        expect(document.querySelector('#github-installation')).not.toBeNull()
      })
      select('github-installation', '222')
    }
    click('Use an existing App')
    expect(document.querySelector<HTMLSelectElement>('#saved-secret')?.value).toBe(secretId)
    expect(container.querySelector<HTMLInputElement>('input[type="checkbox"]')?.checked).toBe(false)
    expect(container.querySelector('#private-key')).toBeNull()
    if (inspected) {
      expect(field('GitHub App ID').value).toBe('111')
      expect(field('Installation ID').value).toBe('222')
    } else {
      await enter('GitHub App ID', '111')
      await enter('Installation ID', '222')
    }
    click('Connect app')
    await waitForUI(() => {
      expect(onConnected).toHaveBeenCalledOnce()
    })
    expect(api.requestsTo('POST', appPath + '/setup')[0]?.body).toMatchObject({
      credential_secret_id: secretId,
    })
    expect(api.requestsTo('POST', `/api/v1/orgs/${orgId}/secrets`)).toHaveLength(0)
  },
)

it.each([
  ['conversion_failed', 'could not retrieve'],
  ['secret_save_failed', 'could not save'],
])('opens manual credential recovery after %s', (reason, message) => {
  window.history.replaceState(null, '', `/?github_setup_error=${reason}`)
  const api = fakeApi(reads)
  render(
    api,
    <ConnectGitHubForm orgId={orgId} projectId={projectId} app={app} onConnected={vi.fn()} />,
  )
  expect(window.location.search).toBe('')
  expect(container.querySelector('[role="alert"]')?.textContent).toContain(message)
  expect(container.querySelector('[role="alert"]')?.textContent).toContain(
    'generate a private key and set a webhook secret',
  )
  expect(container.querySelector('#private-key')).not.toBeNull()
  expect(container.textContent).not.toContain('Continue to GitHub')
  expect(api.requestsTo('POST', appPath + '/github-setup')).toHaveLength(0)
})

it.each([false, true])(
  'registers a customer-owned app with a manifest form POST (organization=%s)',
  async (organization) => {
    const registrationUrl = organization
      ? 'https://github.com/organizations/engineering/settings/apps/new?state=sealed'
      : 'https://github.com/settings/apps/new?state=sealed'
    const manifest = {
      name: 'reviewer',
      public: false,
      default_permissions: { pull_requests: 'write', issues: 'read' },
      default_events: [
        'pull_request',
        'issue_comment',
        'pull_request_review',
        'pull_request_review_comment',
      ],
    }
    const api = fakeApi([
      ...reads,
      {
        method: 'POST',
        path: projectPath + '/apps',
        respond: () => Response.json(app, { status: 201 }),
      },
      {
        method: 'POST',
        path: appPath + '/github-setup',
        respond: () =>
          Response.json(
            {
              app_id: app.id,
              setup_revision: 1,
              registration_url: registrationUrl,
              manifest,
              expires_at: '2026-09-22T01:00:00Z',
            },
            { status: 201 },
          ),
      },
    ])
    const posted: { action: string; method: string; manifest: FormDataEntryValue | null }[] = []
    vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(function (
      this: HTMLFormElement,
    ) {
      posted.push({
        action: this.action,
        method: this.method.toUpperCase(),
        manifest: new FormData(this).get('manifest'),
      })
    })
    render(api, <ConnectGitHubForm orgId={orgId} projectId={projectId} onConnected={vi.fn()} />)
    expect(container.textContent).toContain('private')
    expect(container.querySelector('#provider-display')).toBeNull()
    expect(document.querySelector<HTMLSelectElement>('#github-owner')?.labels[0]?.textContent).toBe(
      'GitHub App owner',
    )
    await enter('App name', 'reviewer')
    if (organization) {
      select('github-owner', 'organization')
      await enter('Organization login', 'engineering')
      click('Use an existing App')
      click('Back to guided setup')
      expect(field('Organization login').value).toBe('engineering')
    }
    click('Continue to GitHub')
    await waitForUI(() => {
      expect(posted).toHaveLength(1)
    })
    expect(api.requestsTo('POST', projectPath + '/apps')[0]?.body).toEqual({
      name: 'reviewer',
      app_type: 'github_pr',
      settings: {},
    })
    expect(api.requestsTo('POST', appPath + '/github-setup')[0]?.body).toEqual(
      organization
        ? { expected_setup_revision: 1, organization: 'engineering' }
        : { expected_setup_revision: 1 },
    )
    expect(posted[0]).toEqual({
      action: registrationUrl,
      method: 'POST',
      manifest: JSON.stringify(manifest),
    })
    expect(api.requestsTo('POST', appPath + '/setup')).toHaveLength(0)
  },
)

it.each(['registration', 'connection'] as const)(
  'disables deletion and mode switching during guided %s',
  async (operation) => {
    if (operation === 'connection')
      window.history.replaceState(null, '', `/?credentials_secret_ref=${secretId}`)
    let release!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => {
      release = resolve
    })
    const path = appPath + (operation === 'registration' ? '/github-setup' : '/setup')
    const api = fakeApi([
      ...reads,
      { method: 'POST', path: inspectPath, respond: () => Response.json(verified) },
      { method: 'POST', path, respond: () => pending },
    ])
    render(
      api,
      <ConnectGitHubForm
        orgId={orgId}
        projectId={projectId}
        app={app}
        onConnected={vi.fn()}
        onCancel={vi.fn()}
        footerAction={<button type="button">Delete app</button>}
      />,
    )
    if (operation === 'connection') {
      click('Check installations')
      await waitForUI(() => {
        expect(document.querySelector('#github-installation')).not.toBeNull()
      })
      select('github-installation', '222')
    }
    click(operation === 'registration' ? 'Continue to GitHub' : 'Connect app')
    await waitForUI(() => {
      expect(api.requestsTo('POST', path)).toHaveLength(1)
    })
    expect(button('Delete app').closest('fieldset')?.disabled).toBe(true)
    expect(button('Use an existing App').disabled).toBe(true)
    expect(button('Cancel').disabled).toBe(true)
    act(() => {
      release(jsonResponse({ code: 'unavailable', error: 'Try again' }, 500))
    })
    await waitForUI(() => {
      expect(button('Use an existing App').disabled).toBe(false)
    })
    expect(button('Delete app').closest('fieldset')?.disabled).toBe(false)
  },
)

it.each([true, false])(
  'reuses the manual draft and credential after a guided detour (new credential=%s)',
  async (newCredential) => {
    let release!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => {
      release = resolve
    })
    let connects = 0
    const secretsPath = `/api/v1/orgs/${orgId}/secrets`
    const saved = credential(secretId, 'Reviewer credentials')
    const api = fakeApi([
      { method: 'GET', path: projectPath + '/secrets', respond: () => credentialPage(saved) },
      ...reads,
      {
        method: 'POST',
        path: projectPath + '/apps',
        respond: () => Response.json(app, { status: 201 }),
      },
      {
        method: 'POST',
        path: secretsPath,
        respond: () => Response.json(saved, { status: 201 }),
      },
      { method: 'POST', path: inspectPath, respond: () => Response.json(verified) },
      {
        method: 'POST',
        path: appPath + '/setup',
        respond: ({ body }) =>
          ++connects === 1
            ? pending
            : Response.json({
                ...app,
                ...schemas.zConfigureProjectAppRequest.parse(body),
                state: 'active',
                setup_revision: 2,
              }),
      },
    ])
    const onConnected = vi.fn()
    render(api, <ConnectGitHubForm orgId={orgId} projectId={projectId} onConnected={onConnected} />)
    await enter('App name', app.name)
    click('Use an existing App')
    expect(field('App name').value).toBe(app.name)
    await enter('GitHub App ID', '111')
    await enter('Installation ID', '222')
    if (newCredential) {
      await enter('RSA private key (PEM)', 'test-key')
      await enter('Webhook secret', 'signature')
    } else {
      act(() => {
        const checkbox = container.querySelector<HTMLInputElement>('input[type="checkbox"]')
        if (!checkbox) throw new Error('Missing credential toggle')
        checkbox.click()
      })
      await waitForUI(() => {
        expect(document.querySelector(`#saved-secret option[value="${secretId}"]`)).not.toBeNull()
      })
      select('saved-secret', secretId)
    }
    click('Create and connect')
    await waitForUI(() => {
      expect(api.requestsTo('POST', appPath + '/setup')).toHaveLength(1)
    })
    expect(button('Back to guided setup').disabled).toBe(true)
    click('Back to guided setup')
    expect(document.querySelector('#provider-tenant')).not.toBeNull()
    act(() => {
      release(jsonResponse({ code: 'unavailable', error: 'Retry verification' }, 500))
    })
    await waitForUI(() => {
      expect(button('Back to guided setup').disabled).toBe(false)
    })
    click('Back to guided setup')
    expect(document.querySelector<HTMLSelectElement>('#saved-secret')?.value).toBe(secretId)
    click('Check installations')
    await waitForUI(() => {
      expect(document.querySelector('#github-installation')).not.toBeNull()
    })
    expect(api.requestsTo('POST', projectPath + '/apps')).toHaveLength(1)
    click('Use an existing App')
    expect(field('GitHub App ID').value).toBe('111')
    expect(field('Installation ID').value).toBe('222')
    if (newCredential)
      expect(container.textContent).toContain('Credentials saved. Retry reuses the saved secret.')
    else expect(document.querySelector<HTMLSelectElement>('#saved-secret')?.value).toBe(secretId)
    click('Create and connect')
    await waitForUI(() => {
      expect(onConnected).toHaveBeenCalledOnce()
    })
    expect(api.requestsTo('POST', projectPath + '/apps')).toHaveLength(1)
    expect(api.requestsTo('POST', secretsPath)).toHaveLength(newCredential ? 1 : 0)
    expect(api.requestsTo('POST', appPath + '/setup').map((request) => request.body)).toEqual([
      {
        expected_setup_revision: 1,
        provider_tenant_id: '111',
        provider_account_ref: '222',
        credential_secret_id: secretId,
        provider_config: {},
      },
      {
        expected_setup_revision: 1,
        provider_tenant_id: '111',
        provider_account_ref: '222',
        credential_secret_id: secretId,
        provider_config: {},
      },
    ])
  },
)
