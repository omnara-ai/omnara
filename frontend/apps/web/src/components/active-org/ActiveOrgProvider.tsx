import { useMe } from '@omnara/react'
import { type ReactNode, useCallback, useEffect, useMemo, useState } from 'react'

import { FullPageSpinner } from '@/components/ui/spinner'
import { ActiveOrgContext, type ActiveOrgValue } from '@/lib/active-org-context'
import { readLocal, writeLocal } from '@/lib/storage'

const STORAGE_KEY = 'omnara-active-org'

export function ActiveOrgProvider({ children }: { children: ReactNode }) {
  const { data: me } = useMe()
  const orgs = me.orgs

  const [selectedOrgId, setSelectedOrgId] = useState(() => {
    const stored = readLocal(STORAGE_KEY)
    return stored && orgs.some((org) => org.id === stored) ? stored : (orgs[0]?.id ?? '')
  })

  const activeOrg = orgs.find((org) => org.id === selectedOrgId) ?? orgs[0]

  useEffect(() => {
    if (selectedOrgId) {
      writeLocal(STORAGE_KEY, selectedOrgId)
    }
  }, [selectedOrgId])

  const setActiveOrgId = useCallback((id: string) => {
    writeLocal(STORAGE_KEY, id)
    setSelectedOrgId(id)
  }, [])

  const value = useMemo<ActiveOrgValue | null>(
    () => (activeOrg ? { orgs, activeOrg, setActiveOrgId } : null),
    [activeOrg, orgs, setActiveOrgId],
  )

  if (!value) return <FullPageSpinner />

  return <ActiveOrgContext.Provider value={value}>{children}</ActiveOrgContext.Provider>
}
