import { Outlet } from '@tanstack/react-router'
import { Suspense, useContext } from 'react'

import { AppShell } from '@/components/app-shell/AppShell'
import { FullPageSpinner } from '@/components/ui/spinner'
import { ActiveOrgContext } from '@/lib/active-org-context'

export function AuthedLayout() {
  const activeOrg = useContext(ActiveOrgContext)
  if (activeOrg == null) return <FullPageSpinner />

  return (
    <AppShell>
      <Suspense fallback={<FullPageSpinner />}>
        <Outlet />
      </Suspense>
    </AppShell>
  )
}
