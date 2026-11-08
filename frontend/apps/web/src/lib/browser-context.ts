import { ApiError, type CurrentUser, type OmnaraClient } from '@omnara/sdk'
import { getCurrentUserQueryKey } from '@omnara/sdk/tanstack'
import { hashKey, type QueryClient } from '@tanstack/react-query'

declare global {
  interface Window {
    omnaraBrowserContext?: {
      readonly userId: string
      readonly organizationIds: readonly string[]
    }
  }
}

export function installBrowserContext(
  target: Window,
  queryClient: QueryClient,
  client: OmnaraClient,
) {
  const key = getCurrentUserQueryKey({ client })
  const hash = hashKey(key)
  let current: Window['omnaraBrowserContext']

  function publish() {
    const state = queryClient.getQueryState<CurrentUser>(key)
    const me =
      state?.error instanceof ApiError && state.error.status === 401 ? undefined : state?.data
    const organizationIds = me?.orgs.map((org) => org.id).sort() ?? []
    if (
      current?.userId === me?.user.id &&
      (current?.organizationIds.join(',') ?? '') === organizationIds.join(',')
    )
      return

    current = me
      ? Object.freeze({
          userId: me.user.id,
          organizationIds: Object.freeze(organizationIds),
        })
      : undefined
    target.omnaraBrowserContext = current
    target.dispatchEvent(new Event('omnara:browser-context'))
  }

  const unsubscribe = queryClient.getQueryCache().subscribe((event) => {
    if (
      event.query.queryHash === hash &&
      event.type === 'updated' &&
      (event.action.type === 'success' || event.action.type === 'error')
    )
      publish()
  })
  publish()
  return unsubscribe
}
