import { use } from 'react'

import { ActiveOrgContext, type ActiveOrgValue } from '@/lib/active-org-context'

export function useActiveOrg(): ActiveOrgValue {
  const ctx = use(ActiveOrgContext)
  if (!ctx) {
    throw new Error('useActiveOrg must be used within an ActiveOrgProvider')
  }
  return ctx
}
