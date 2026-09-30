import {
  useConfigureIntegration,
  useCreateIntegrationGitHubSetup,
  useInspectIntegrationGitHubInstallations,
} from '@omnara/react'
import type { CreateGitHubSetupRequest, GitHubInstallations, Integration } from '@omnara/sdk'
import { useEffect, useEffectEvent, useRef, useState } from 'react'

import { integrationFormError } from './integrationFormState'
import type { useIntegrationSetupState } from './useIntegrationSetupState'

export interface GitHubInspection {
  result: GitHubInstallations
  page: number
}

export function useGitHubGuidedSetup({
  orgId,
  projectId,
  ensureIntegration,
  session,
  installationHint,
  returnedFromGitHub,
  onConnected,
}: {
  orgId: string
  projectId: string
  ensureIntegration: () => Promise<Integration>
  session: ReturnType<typeof useIntegrationSetupState>
  installationHint: string
  returnedFromGitHub: boolean
  onConnected: (integration: Integration) => void
}) {
  const { run, setError } = session
  const secretId = session.savedSecret || session.selectedSecret
  const [resuming, setResuming] = useState(Boolean(secretId))
  const [returned, setReturned] = useState(returnedFromGitHub && Boolean(secretId))
  const [organizationOwned, setOrganizationOwned] = useState(false)
  const [organization, setOrganization] = useState('')
  const [inspection, setInspection] = useState<GitHubInspection>()
  const [installationId, setInstallationId] = useState(installationHint)
  const start = useCreateIntegrationGitHubSetup(orgId, projectId)
  const inspect = useInspectIntegrationGitHubInstallations(orgId, projectId)
  const configure = useConfigureIntegration(orgId, projectId)
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const isMounted = () => mounted.current
  const checkReturnedAccess = useEffectEvent(() => {
    if (returned) inspectInstallations()
  })
  const returnChecked = useRef(false)
  useEffect(() => {
    if (returnChecked.current) return
    returnChecked.current = true
    checkReturnedAccess()
  }, [])
  const inspected = inspection?.result
  const selected = inspected?.installations.find(
    (installation) => installation.id === installationId,
  )

  function clearInspection() {
    setInspection(undefined)
    setInstallationId('')
  }

  function register() {
    void run(
      async () => {
        const draft = await ensureIntegration()
        if (!isMounted()) return
        const request: CreateGitHubSetupRequest & { integrationID: string } = {
          integrationID: draft.id,
          expected_setup_revision: draft.setup_revision,
        }
        if (organizationOwned) request.organization = organization.trim()
        const registration = await start.mutateAsync(request)
        if (!isMounted()) return
        if (registration.integration_id !== draft.id)
          throw new Error('Registration returned a different integration. Please try again.')
        const form = document.createElement('form')
        form.method = 'POST'
        form.action = registration.registration_url
        const manifest = document.createElement('input')
        manifest.type = 'hidden'
        manifest.name = 'manifest'
        manifest.value = JSON.stringify(registration.manifest)
        form.append(manifest)
        document.body.append(form)
        form.submit()
        form.remove()
      },
      (cause) => integrationFormError(cause, 'Could not start GitHub registration.'),
    )
  }

  function inspectInstallations(page = 1) {
    if (!secretId) return
    void run(
      async () => {
        const draft = await ensureIntegration()
        if (!isMounted()) return
        const result = await inspect.mutateAsync({
          integrationID: draft.id,
          credential_secret_id: secretId,
          page,
        })
        if (!isMounted()) return
        const sole =
          page === 1 && !result.next_page && result.installations.length === 1
            ? result.installations[0]?.id
            : undefined
        setInspection({ result, page })
        setInstallationId((current) =>
          result.installations.some((installation) => installation.id === current)
            ? current
            : (sole ?? ''),
        )
      },
      (cause) => {
        clearInspection()
        setReturned(false)
        return integrationFormError(cause, 'Could not check GitHub access.')
      },
    )
  }

  function connect() {
    if (!selected || !inspected) return
    void run(
      async () => {
        const draft = await ensureIntegration()
        if (!isMounted()) return
        const saved = await configure.mutateAsync({
          integrationID: draft.id,
          expected_setup_revision: draft.setup_revision,
          provider_tenant_id: inspected.provider_app_id,
          provider_account_ref: selected.id,
          credential_secret_id: secretId,
        })
        if (isMounted()) onConnected(saved)
      },
      (cause) => integrationFormError(cause, 'Could not connect this GitHub installation.'),
    )
  }

  function continueSetup() {
    if (inspected) connect()
    else if (resuming) inspectInstallations()
    else register()
  }

  function changeCredential(id: string) {
    session.setSavedSecret('')
    session.setSelectedSecret(id)
    session.setNewCredential(false)
    setReturned(false)
    clearInspection()
    setError('')
  }

  function switchCredentialSource() {
    setResuming(!resuming)
    setReturned(false)
    clearInspection()
    setError('')
  }

  function returnToGuided() {
    setResuming(Boolean(secretId))
    clearInspection()
    setError('')
  }

  function handOffToManual() {
    if (secretId) session.setNewCredential(false)
    if (inspected) session.setTenant(inspected.provider_app_id)
    if (selected) session.setAccount(selected.id)
  }

  return {
    secretId,
    resuming,
    returned,
    organizationOwned,
    setOrganizationOwned,
    organization,
    setOrganization,
    inspection,
    selected,
    ready: inspected ? Boolean(selected) : !resuming || Boolean(secretId),
    selectInstallation: setInstallationId,
    inspectInstallations,
    continueSetup,
    changeCredential,
    switchCredentialSource,
    returnToGuided,
    handOffToManual,
  }
}
