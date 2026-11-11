import { ApiError, type CurrentUser, type OmnaraClient } from '@omnara/sdk'
import { getCurrentUserOptions } from '@omnara/sdk/tanstack'
import type { QueryClient } from '@tanstack/react-query'
import { redirect } from '@tanstack/react-router'

export interface RouterContext {
  queryClient: QueryClient
  omnaraClient: OmnaraClient
}

export async function ensureMe(
  queryClient: QueryClient,
  omnaraClient: OmnaraClient,
  returnTo: string,
): Promise<CurrentUser> {
  try {
    return await queryClient.query({
      ...getCurrentUserOptions({ client: omnaraClient }),
      staleTime: 'static',
    })
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) {
      // eslint-disable-next-line @typescript-eslint/only-throw-error -- TanStack Router throws redirects.
      throw redirect({ href: `/login?return_to=${encodeURIComponent(returnTo)}` })
    }
    throw error
  }
}

export async function redirectSignedInStaleCallback({
  context: { queryClient, omnaraClient },
  location,
}: {
  context: RouterContext
  location: { href: string }
}) {
  const authError = new URL(location.href, window.location.origin).searchParams.get('auth_error')
  if (authError !== 'invalid_state') return
  try {
    await queryClient.query({
      ...getCurrentUserOptions({ client: omnaraClient }),
      staleTime: 'static',
    })
  } catch {
    return
  }
  // eslint-disable-next-line @typescript-eslint/only-throw-error -- TanStack Router throws redirects.
  throw redirect({ href: '/' })
}
