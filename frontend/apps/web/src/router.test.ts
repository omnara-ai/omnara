/** @vitest-environment happy-dom */

import { createOmnaraClient } from '@omnara/sdk'
import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'

import { redirectSignedInStaleCallback } from '@/router'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { currentUser } from '@/test/fixtures'

function loginBeforeLoad(search: string, signedIn: boolean) {
  const api = fakeApi([
    {
      method: 'GET',
      path: '/api/v1/me',
      respond: () =>
        signedIn
          ? jsonResponse(currentUser([]))
          : jsonResponse({ code: 'unauthorized', error: 'unauthorized' }, 401),
    },
  ])
  const omnaraClient = createOmnaraClient({
    baseUrl: 'https://omnara.test/api/v1',
    fetch: api.fetch,
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const result = redirectSignedInStaleCallback({
    context: { queryClient, omnaraClient },
    location: { href: `/login${search}` },
  })
  return { api, result }
}

describe('login route', () => {
  it('sends a signed-in user with a stale sign-in callback into the app', async () => {
    const { result } = loginBeforeLoad('?auth_connector=github&auth_error=invalid_state', true)
    await expect(result).rejects.toMatchObject({ options: { href: '/' } })
  })

  it('shows the stale sign-in error to a signed-out user', async () => {
    const { result } = loginBeforeLoad('?auth_connector=github&auth_error=invalid_state', false)
    await expect(result).resolves.toBeUndefined()
  })

  it('keeps other sign-in errors on the page without checking the session', async () => {
    const { api, result } = loginBeforeLoad('?auth_error=email_not_verified', true)
    await expect(result).resolves.toBeUndefined()
    expect(api.requestsTo('GET', '/api/v1/me')).toHaveLength(0)
  })
})
