import type { IntegrationOAuthSetup } from '@omnara/sdk'
import { useEffect, useState } from 'react'

export function useSlackAuthorization() {
  const [pending, setPending] = useState<IntegrationOAuthSetup>()
  const [expired, setExpired] = useState(false)
  useEffect(() => {
    if (!pending) return
    const timer = window.setTimeout(
      () => {
        setExpired(true)
      },
      Math.max(0, Date.parse(pending.expires_at) - Date.now()),
    )
    return () => {
      window.clearTimeout(timer)
    }
  }, [pending])
  return {
    pending,
    expired,
    start: (setup?: IntegrationOAuthSetup) => {
      setExpired(false)
      setPending(setup)
    },
  }
}
