import { Outlet } from '@tanstack/react-router'
import { Suspense } from 'react'

import { ActiveOrgProvider } from '@/components/active-org/ActiveOrgProvider'
import { AgentPanel } from '@/components/agent-panel/AgentPanel'
import { FullPageSpinner } from '@/components/ui/spinner'

export function AuthenticatedLayout() {
  return (
    <ActiveOrgProvider>
      <AgentPanel>
        <Suspense fallback={<FullPageSpinner />}>
          <Outlet />
        </Suspense>
      </AgentPanel>
    </ActiveOrgProvider>
  )
}
