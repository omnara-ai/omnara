/** @vitest-environment happy-dom */

import { ApiError, createOmnaraClient } from '@omnara/sdk'
import { getCurrentUserQueryKey } from '@omnara/sdk/tanstack'
import { QueryClient } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'

import { installBrowserContext } from '@/lib/browser-context'
import { currentUser, currentUserOrg, fakeId } from '@/test/fixtures'

const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
const key = getCurrentUserQueryKey({ client })
const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } })
let unsubscribe: () => void

afterEach(() => {
  unsubscribe()
  queries.clear()
  delete window.omnaraBrowserContext
})

it('publishes user and organization changes without starting requests or repeating unchanged data', () => {
  const changed = vi.fn()
  window.addEventListener('omnara:browser-context', changed)
  unsubscribe = installBrowserContext(window, queries, client)
  try {
    expect(window.omnaraBrowserContext).toBeUndefined()
    expect(changed).not.toHaveBeenCalled()
    Object.assign(window, { omnaraBrowserContext: { organizationIds: null } })
    queries.setQueryData(key, currentUser([]))
    expect(window.omnaraBrowserContext).toEqual({ userId: fakeId('usr'), organizationIds: [] })
    queries.setQueryData(key, currentUser([currentUserOrg()]))
    expect(window.omnaraBrowserContext).toEqual({
      userId: fakeId('usr'),
      organizationIds: [fakeId('org')],
    })
    queries.setQueryData(key, currentUser([currentUserOrg()]))
    queries.setQueryData(['unrelated'], { value: true })
    expect(changed).toHaveBeenCalledTimes(2)
    expect(queries.isFetching()).toBe(0)
    queries.removeQueries({ queryKey: key })
    expect(window.omnaraBrowserContext?.userId).toBe(fakeId('usr'))
    expect(changed).toHaveBeenCalledTimes(2)
  } finally {
    window.removeEventListener('omnara:browser-context', changed)
  }
})

it.each([401, 503])('handles a %s response after publishing cached identity', async (status) => {
  queries.setQueryData(key, currentUser([currentUserOrg()]))
  unsubscribe = installBrowserContext(window, queries, client)
  expect(window.omnaraBrowserContext?.userId).toBe(fakeId('usr'))
  await expect(
    queries.fetchQuery({
      queryKey: key,
      queryFn: () => Promise.reject(new ApiError(status, 'Unavailable')),
    }),
  ).rejects.toThrow()
  expect(window.omnaraBrowserContext?.userId).toBe(status === 401 ? undefined : fakeId('usr'))
})
