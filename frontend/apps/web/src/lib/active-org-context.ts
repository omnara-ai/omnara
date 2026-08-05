import type { CurrentUserOrg } from '@omnara/sdk'
import { createContext } from 'react'

export interface ActiveOrgValue {
  orgs: CurrentUserOrg[]
  activeOrg: CurrentUserOrg
  setActiveOrgId: (id: string) => void
}

export const ActiveOrgContext = createContext<ActiveOrgValue | null>(null)
