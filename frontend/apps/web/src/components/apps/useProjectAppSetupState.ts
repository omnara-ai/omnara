import type { ProjectApp } from '@omnara/sdk'
import { useRef, useState } from 'react'

export function useProjectAppSetupState(
  existing?: ProjectApp,
  initial: { credentialSecretId?: string; error?: string } = {},
) {
  const credentialSecretId = initial.credentialSecretId ?? existing?.credential_secret_id ?? ''
  const [newCredential, setNewCredential] = useState(!credentialSecretId)
  const [savedSecret, setSavedSecret] = useState('')
  const [selectedSecret, setSelectedSecret] = useState(credentialSecretId)
  const [tenant, setTenant] = useState(existing?.provider_tenant_id ?? '')
  const [account, setAccount] = useState(existing?.provider_account_ref ?? '')
  const [error, setError] = useState(initial.error ?? '')
  const [busy, setBusy] = useState(false)
  const running = useRef(false)
  async function run(step: () => Promise<void>, onError: (cause: unknown) => string) {
    if (running.current) return
    running.current = true
    setBusy(true)
    setError('')
    try {
      await step()
    } catch (cause) {
      setError(onError(cause))
    } finally {
      running.current = false
      setBusy(false)
    }
  }
  return {
    newCredential,
    setNewCredential,
    savedSecret,
    setSavedSecret,
    selectedSecret,
    setSelectedSecret,
    tenant,
    setTenant,
    account,
    setAccount,
    error,
    setError,
    busy,
    run,
  }
}
