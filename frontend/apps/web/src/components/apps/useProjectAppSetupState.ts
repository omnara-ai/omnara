import type { ProjectApp } from '@omnara/sdk'
import { useState } from 'react'

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
    setBusy,
  }
}
