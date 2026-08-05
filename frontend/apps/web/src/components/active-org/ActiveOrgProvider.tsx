import { useMe } from '@omnara/react'
import { type ReactNode, useEffect, useState } from 'react'

import { FullPageSpinner } from '@/components/ui/spinner'
import { ActiveOrgContext, type ActiveOrgValue } from '@/lib/active-org-context'
import { readLocal, writeLocal } from '@/lib/storage'

const STORAGE_KEY = 'omnara-active-org'

export function ActiveOrgProvider({ children }: { children: ReactNode }) {
  const { data: me } = useMe()
  const orgs = me.orgs

  const [activeId, setActiveId] = useState(() => {
    const stored = readLocal(STORAGE_KEY)
    return stored && orgs.some((org) => org.id === stored) ? stored : (orgs[0]?.id ?? '')
  })

  const activeOrg = orgs.find((org) => org.id === activeId) ?? orgs[0]

  useEffect(() => {
    if (activeOrg) {
      writeLocal(STORAGE_KEY, activeOrg.id)
    }
  }, [activeOrg])

  if (!activeOrg) {
    return <FullPageSpinner />
  }

  const value: ActiveOrgValue = {
    orgs,
    activeOrg,
    setActiveOrgId: (id) => {
      setActiveId(id)
    },
  }

  return <ActiveOrgContext.Provider value={value}>{children}</ActiveOrgContext.Provider>
}
