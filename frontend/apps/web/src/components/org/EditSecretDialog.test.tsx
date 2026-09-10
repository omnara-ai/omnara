/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type OAuthTokenSetSecretMaterial, type Secret } from '@omnara/sdk'
import { listProjectAvailableSecretsQueryKey, listSecretsQueryKey } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { EditSecretDialog } from '@/components/org/EditSecretDialog'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, field, submit, waitForUI } from '@/test/secret-editor'

const secret: Secret = {
  id: fakeId('sec'),
  org_id: fakeId('org'),
  management_kind: 'tenant',
  owner: { kind: 'org' },
  name: 'token',
  kind: 'generic',
  metadata: {},
  current_version_number: 1,
  payload_keys: ['value'],
  created_at: '2026-08-03T00:00:00Z',
  updated_at: '2026-08-03T00:00:00Z',
}
let root: Root
let container: HTMLDivElement
let restore: () => void
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
})
async function render(
  overrides: Partial<Secret> = {},
  failFirst = false,
  gates?: { rename: Promise<void>; value: Promise<void> },
) {
  let failures = failFirst ? 1 : 0
  const path = `/api/v1/orgs/${secret.org_id}/secrets/${secret.id}`
  const api = fakeApi([
    {
      method: 'PATCH',
      path,
      respond: async () => {
        if (gates) await gates.rename
        return failures-- > 0
          ? jsonResponse({ code: 'conflict', error: 'Try again' }, 409)
          : jsonResponse({ ...secret, ...overrides, name: 'renamed', current_version_number: 2 })
      },
    },
    {
      method: 'POST',
      path: path + '/versions',
      respond: async () => {
        if (gates) await gates.value
        return failures-- > 0
          ? jsonResponse({ code: 'conflict', error: 'Try again' }, 409)
          : jsonResponse({ ...secret, ...overrides, current_version_number: 2 })
      },
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const key = listSecretsQueryKey({ path: { orgID: secret.org_id } })
  const projectKey = listProjectAvailableSecretsQueryKey({
    path: { orgID: secret.org_id, projectID: fakeId('prj') },
  })
  const otherKey = listSecretsQueryKey({ path: { orgID: 'other' } })
  for (const k of [key, projectKey, otherKey])
    queryClient.setQueryData(k, { data: [], next_cursor: null })
  const closed = vi.fn()
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <EditSecretDialog
            open
            onOpenChange={closed}
            orgId={secret.org_id}
            secret={{ ...secret, ...overrides }}
          />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await Promise.resolve()
  })
  return { api, path, closed, queryClient, key, projectKey, otherKey }
}
it('shows a masked stored field immediately and focus does not enable save', async () => {
  await render()
  const value = field('Value')
  expect(value.value).toBe('')
  expect(value.placeholder).toBe('••••••••••••')
  act(() => {
    value.focus()
  })
  const save = [...document.querySelectorAll('button')].find(
    (b) => b.textContent === 'Save changes',
  )
  expect(save?.disabled).toBe(true)
})
it('saves name and value through existing endpoints and invalidates both secret caches', async () => {
  const ctx = await render()
  await enter('Name', 'renamed')
  await enter('Value', 'new-token')
  await submit()
  expect(ctx.api.requestsTo('PATCH', ctx.path).map((r) => r.body)).toEqual([{ name: 'renamed' }])
  expect(ctx.api.requestsTo('POST', ctx.path + '/versions').map((r) => r.body)).toEqual([
    { material: { kind: 'generic', value: 'new-token' } },
  ])
  expect(ctx.closed).toHaveBeenCalledWith(false)
  expect(ctx.queryClient.getQueryState(ctx.key)?.isInvalidated).toBe(true)
  expect(ctx.queryClient.getQueryState(ctx.projectKey)?.isInvalidated).toBe(true)
  expect(ctx.queryClient.getQueryState(ctx.otherKey)?.isInvalidated).toBe(false)
})
it('Undo restores the untouched field and prevents value submission', async () => {
  const ctx = await render()
  await enter('Value', 'discard-me')
  act(() => {
    button('Undo value change').click()
  })
  expect(field('Value').placeholder).toBe('••••••••••••')
  await enter('Name', 'renamed')
  await submit()
  expect(ctx.api.requestsTo('PATCH', ctx.path).map((r) => r.body)).toEqual([{ name: 'renamed' }])
})
it('keeps input on failure and retries the value request', async () => {
  const ctx = await render({}, true)
  await enter('Value', 'new-token')
  await submit()
  expect(ctx.closed).not.toHaveBeenCalled()
  expect(field('Value').value).toBe('new-token')
  await submit()
  expect(ctx.api.requestsTo('POST', ctx.path + '/versions')).toHaveLength(2)
  expect(ctx.closed).toHaveBeenCalledWith(false)
})
it('requires complete AWS credentials', async () => {
  const ctx = await render({
    kind: 'aws_credentials',
    payload_keys: ['access_key_id', 'secret_access_key', 'session_token'],
  })
  await enter('Secret access key', 'new-key')
  await submit()
  expect(ctx.api.requestsTo('POST', ctx.path + '/versions')).toHaveLength(0)
  await enter('Access key ID', ' new-id ')
  await enter('Session token', 'new-session')
  await submit()
  expect(ctx.api.requestsTo('POST', ctx.path + '/versions').map((r) => r.body)).toEqual([
    {
      material: {
        kind: 'aws_credentials',
        access_key_id: 'new-id',
        secret_access_key: 'new-key',
        session_token: 'new-session',
      },
    },
  ])
})
it('Clear explicitly removes a stored optional field on save', async () => {
  const ctx = await render({
    kind: 'aws_credentials',
    payload_keys: ['access_key_id', 'secret_access_key', 'session_token'],
  })
  act(() => {
    button('Clear session token').click()
  })
  expect(field('Session token').value).toBe('')
  await submit()
  expect(ctx.api.requestsTo('POST', ctx.path + '/versions')).toHaveLength(0)
  await enter('Access key ID', ' new-id ')
  await enter('Secret access key', 'new-key')
  await submit()
  expect(ctx.api.requestsTo('POST', ctx.path + '/versions').map((r) => r.body)).toEqual([
    {
      material: { kind: 'aws_credentials', access_key_id: 'new-id', secret_access_key: 'new-key' },
    },
  ])
})
it('Undo restores a cleared field to its unchanged stored state', async () => {
  const ctx = await render({
    kind: 'aws_credentials',
    payload_keys: ['access_key_id', 'secret_access_key', 'session_token'],
  })
  act(() => {
    button('Clear session token').click()
  })
  act(() => {
    button('Undo session token change').click()
  })
  expect(field('Session token').placeholder).toBe('••••••••••••')
  expect(button('Save changes').disabled).toBe(true)
  await enter('Name', 'renamed')
  await submit()
  expect(ctx.api.requestsTo('POST', ctx.path + '/versions')).toHaveLength(0)
})

it.each([{ kind: 'slack_app_credentials' as const }, { management_kind: 'cluster' as const }])(
  'hides unsupported value editing %j',
  async (overrides) => {
    await render(overrides)
    expect([...document.querySelectorAll('label')].map((label) => label.textContent)).toEqual([
      'Name',
    ])
  },
)

it('preserves multiline generic values through reveal and save', async () => {
  const ctx = await render()
  const value = 'line one\nline two\nline three'
  await enter('Value', value)
  act(() => {
    button('Show value').click()
  })
  expect(field('Value').value).toBe(value)
  await submit()
  expect(ctx.api.requestsTo('POST', ctx.path + '/versions').map((r) => r.body)).toEqual([
    { material: { kind: 'generic', value } },
  ])
})

it('masks a new draft after clearing a revealed value', async () => {
  await render()
  await enter('Value', 'first')
  act(() => {
    button('Show value').click()
  })
  await enter('Value', '')
  await enter('Value', 'second')
  expect(field('Value').className).toContain('text-security:disc')
  expect(document.querySelector('button[aria-label="Show value"]')).not.toBeNull()
})

it.each(['', '3600'])(
  'submits complete OAuth refresh credentials with lifetime %j',
  async (lifetime) => {
    const ctx = await render({
      kind: 'oauth_token_set',
      payload_keys: [
        'access_token',
        'refresh_token',
        'client_id',
        'client_secret',
        'resource',
        'token_endpoint',
        'scopes',
      ],
    })
    for (const [label, value] of Object.entries({
      'Access token': 'new-access',
      'Refresh token': 'new-refresh',
      'Client ID': 'client',
      'Client secret': 'client-secret',
      Resource: 'https://example.com/api',
      'Token endpoint': 'https://example.com/token',
      Scopes: 'read write',
    }))
      await enter(label, value)
    await enter('Access token lifetime (seconds)', lifetime)
    await submit()
    const material: OAuthTokenSetSecretMaterial = {
      kind: 'oauth_token_set',
      access_token: 'new-access',
      scopes: 'read write',
      refresh: {
        refresh_token: 'new-refresh',
        client_id: 'client',
        client_secret: 'client-secret',
        resource: 'https://example.com/api',
        token_endpoint: 'https://example.com/token',
      },
    }
    if (lifetime !== '') material.access_token_expires_in_seconds = 3600
    expect(
      ctx.api.requestsTo('POST', ctx.path + '/versions').map((request) => request.body),
    ).toEqual([{ material }])
    expect(ctx.api.requestsTo('PATCH', ctx.path)).toHaveLength(0)
  },
)

it('blocks resubmission and dismissal throughout both saves', async () => {
  function gate() {
    let release: () => void = () => {
      throw new Error('Gate not initialized')
    }
    const promise = new Promise<void>((resolve) => {
      release = resolve
    })
    return { promise, release }
  }
  const rename = gate()
  const value = gate()
  const ctx = await render({}, false, { rename: rename.promise, value: value.promise })
  await enter('Name', 'renamed')
  await enter('Value', 'replacement')
  const form = document.querySelector('form')
  if (!form) throw new Error('Missing form')
  const dispatchSubmit = () =>
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  act(() => {
    dispatchSubmit()
  })
  await waitForUI(() => {
    expect(ctx.api.requestsTo('PATCH', ctx.path)).toHaveLength(1)
  })
  for (const stage of ['rename', 'value']) {
    expect(form.querySelector('fieldset')?.disabled).toBe(true)
    expect(button('Cancel').closest('fieldset')?.disabled).toBe(true)
    act(() => {
      dispatchSubmit()
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    expect(ctx.closed).not.toHaveBeenCalled()
    expect(document.querySelector('[role="dialog"]')).not.toBeNull()
    if (stage === 'rename') {
      await act(async () => {
        rename.release()
        await rename.promise
      })
      await waitForUI(() => {
        expect(ctx.api.requestsTo('POST', ctx.path + '/versions')).toHaveLength(1)
      })
    }
  }
  await act(async () => {
    value.release()
    await value.promise
  })
  await waitForUI(() => {
    expect(ctx.closed).toHaveBeenCalledWith(false)
  })
  expect(ctx.api.requestsTo('PATCH', ctx.path)).toHaveLength(1)
  expect(ctx.api.requestsTo('POST', ctx.path + '/versions')).toHaveLength(1)
})

it.each(['Backspace', 'Delete'])('leaves untouched credentials unchanged on %s', async (key) => {
  const ctx = await render({
    kind: 'aws_credentials',
    payload_keys: ['access_key_id', 'secret_access_key', 'session_token'],
  })
  for (const label of ['Access key ID', 'Session token']) {
    act(() => {
      field(label).dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
    })
  }
  expect(document.querySelector('[role="alert"]')).toBeNull()
  expect(document.querySelector('button[aria-label^="Undo"]')).toBeNull()
  expect(button('Save changes').disabled).toBe(true)
  await enter('Name', 'renamed')
  await submit()
  expect(ctx.api.requestsTo('PATCH', ctx.path)).toHaveLength(1)
  expect(ctx.api.requestsTo('POST', ctx.path + '/versions')).toHaveLength(0)
})
