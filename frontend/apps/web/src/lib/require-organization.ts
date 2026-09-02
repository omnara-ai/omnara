import type { CurrentUser } from '@omnara/sdk'
import { redirect } from '@tanstack/react-router'

export function requireOrganization(me: CurrentUser, returnTo: string): void {
  if (me.orgs.length > 0) return
  const search = returnTo === '/' ? '' : `?return_to=${encodeURIComponent(returnTo)}`
  // eslint-disable-next-line @typescript-eslint/only-throw-error -- TanStack Router throws redirects.
  throw redirect({ href: `/onboarding${search}` })
}
