import {
  useConfigureProjectApp,
  useCreateProjectAppGitHubSetup,
  useInspectProjectAppGitHubInstallations,
} from '@omnara/react'
import type { CreateGitHubSetupRequest, GitHubInstallations, ProjectApp } from '@omnara/sdk'
import { useEffect, useRef, useState } from 'react'

import { projectAppFormError } from './projectAppFormState'
import type { useProjectAppSetupState } from './useProjectAppSetupState'

export interface GitHubInspection {
  result: GitHubInstallations
  page: number
}

export function useGitHubGuidedSetup({
  orgId,
  projectId,
  ensureApp,
  session,
  installationHint,
  onConnected,
}: {
  orgId: string
  projectId: string
  ensureApp: () => Promise<ProjectApp>
  session: ReturnType<typeof useProjectAppSetupState>
  installationHint: string
  onConnected: (app: ProjectApp) => void
}) {
  const { run, setError } = session
  const secretId = session.savedSecret || session.selectedSecret
  const [resuming, setResuming] = useState(Boolean(secretId))
  const [organizationOwned, setOrganizationOwned] = useState(false)
  const [organization, setOrganization] = useState('')
  const [inspection, setInspection] = useState<GitHubInspection>()
  const [installationId, setInstallationId] = useState(installationHint)
  const start = useCreateProjectAppGitHubSetup(orgId, projectId)
  const inspect = useInspectProjectAppGitHubInstallations(orgId, projectId)
  const configure = useConfigureProjectApp(orgId, projectId)
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
        const draft = await ensureApp()
        if (!isMounted()) return
        const request: CreateGitHubSetupRequest & { appID: string } = {
          appID: draft.id,
          expected_setup_revision: draft.setup_revision,
        }
        if (organizationOwned) request.organization = organization.trim()
        const registration = await start.mutateAsync(request)
        if (!isMounted()) return
        if (registration.app_id !== draft.id)
          throw new Error('Registration returned a different app. Please try again.')
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
      (cause) => projectAppFormError(cause, 'Could not start GitHub registration.'),
    )
  }

  function inspectInstallations(page = 1) {
    if (!secretId) return
    void run(
      async () => {
        const draft = await ensureApp()
        if (!isMounted()) return
        const result = await inspect.mutateAsync({
          appID: draft.id,
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
        return projectAppFormError(cause, 'Could not check GitHub installations.')
      },
    )
  }

  function connect() {
    if (!selected || !inspected) return
    void run(
      async () => {
        const draft = await ensureApp()
        if (!isMounted()) return
        const saved = await configure.mutateAsync({
          appID: draft.id,
          expected_setup_revision: draft.setup_revision,
          provider_tenant_id: inspected.provider_app_id,
          provider_account_ref: selected.id,
          credential_secret_id: secretId,
        })
        if (isMounted()) onConnected(saved)
      },
      (cause) => projectAppFormError(cause, 'Could not connect this GitHub installation.'),
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
