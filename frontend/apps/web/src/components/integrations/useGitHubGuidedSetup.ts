import {
  useConfigureProjectIntegration,
  useCreateProjectIntegrationGitHubSetup,
  useInspectProjectIntegrationGitHubInstallations,
} from '@omnara/react'
import type { CreateGitHubSetupRequest, GitHubInstallations, ProjectIntegration } from '@omnara/sdk'
import { useEffect, useRef, useState } from 'react'

import { projectIntegrationFormError } from './projectIntegrationFormState'
import type { useProjectIntegrationSetupState } from './useProjectIntegrationSetupState'

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
  onConnected,
}: {
  orgId: string
  projectId: string
  ensureIntegration: () => Promise<ProjectIntegration>
  session: ReturnType<typeof useProjectIntegrationSetupState>
  installationHint: string
  onConnected: (integration: ProjectIntegration) => void
}) {
  const { run, setError } = session
  const secretId = session.savedSecret || session.selectedSecret
  const [resuming, setResuming] = useState(Boolean(secretId))
  const [organizationOwned, setOrganizationOwned] = useState(false)
  const [organization, setOrganization] = useState('')
  const [inspection, setInspection] = useState<GitHubInspection>()
  const [installationId, setInstallationId] = useState(installationHint)
  const start = useCreateProjectIntegrationGitHubSetup(orgId, projectId)
  const inspect = useInspectProjectIntegrationGitHubInstallations(orgId, projectId)
  const configure = useConfigureProjectIntegration(orgId, projectId)
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const isMounted = () => mounted.current
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
      (cause) => projectIntegrationFormError(cause, 'Could not start GitHub registration.'),
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
          credentials_secret_ref: secretId,
          page,
        })
        if (!isMounted()) return
        setInspection({ result, page })
        if (!result.installations.some((installation) => installation.id === installationId))
          setInstallationId('')
      },
      (cause) => {
        clearInspection()
        return projectIntegrationFormError(cause, 'Could not check GitHub installations.')
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
      (cause) => projectIntegrationFormError(cause, 'Could not connect this GitHub installation.'),
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
    clearInspection()
    setError('')
  }

  function switchCredentialSource() {
    setResuming(!resuming)
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
