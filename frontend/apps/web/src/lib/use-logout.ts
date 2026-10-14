import { sessionLogout } from '@omnara/sdk/browser'
import { useState } from 'react'

// Logout redirects on success, so there is no success state.
type LogoutStatus = { kind: 'idle' } | { kind: 'pending' } | { kind: 'error'; message: string }

export function useLogout() {
  const [status, setStatus] = useState<LogoutStatus>({ kind: 'idle' })

  async function logout() {
    setStatus({ kind: 'pending' })
    try {
      await sessionLogout()
      window.location.href = '/login'
    } catch {
      setStatus({ kind: 'error', message: 'Logout failed' })
    }
  }

  return { status, logout }
}
