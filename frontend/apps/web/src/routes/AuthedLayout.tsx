import { Outlet } from '@tanstack/react-router'
import { Suspense } from 'react'

import { ActiveOrgProvider } from '@/components/active-org/ActiveOrgProvider'
import { AppShell } from '@/components/app-shell/AppShell'
import { FullPageSpinner } from '@/components/ui/spinner'

export function AuthedLayout() {
  return (
    <ActiveOrgProvider>
      <AppShell>
        <Suspense fallback={<FullPageSpinner />}>
          <Outlet />
        </Suspense>
      </AppShell>
    </ActiveOrgProvider>
  )
}
