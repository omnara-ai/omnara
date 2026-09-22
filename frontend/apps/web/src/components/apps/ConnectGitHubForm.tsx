import {
  useConfigureProjectApp,
  useCreateProjectAppGitHubSetup,
  useInspectProjectAppGitHubInstallations,
} from '@omnara/react'
import type { CreateGitHubSetupRequest, GitHubInstallations, ProjectApp } from '@omnara/sdk'
import { type ReactNode, useEffect, useRef, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/submit-status'

import { ProjectAppNameField } from './ProjectAppNameField'
import { ProjectAppSetupForm } from './ProjectAppSetup'
import { ProjectAppCredentialPicker } from './ProjectAppSetupCredentials'
import { useGitHubSetupReturn } from './useGitHubSetupReturn'
import { useProjectAppDraft } from './useProjectAppDraft'
import { useProjectAppSetupState } from './useProjectAppSetupState'

const selectClass =
  'control-focus rounded-control border-input bg-card h-10 w-full border px-3 text-sm'

export function ConnectGitHubForm({
  orgId,
  projectId,
  app: existing,
  onConnected,
  onCancel,
  footerAction,
}: {
  orgId: string
  projectId: string
  app?: ProjectApp
  onConnected: (app: ProjectApp) => void
  onCancel?: () => void
  footerAction?: ReactNode
}) {
  const draft = useProjectAppDraft(orgId, projectId, 'github_pr', existing)
  const { app, name, setName, ensureApp } = draft
  const returned = useGitHubSetupReturn()
  const manualSetup = useProjectAppSetupState(existing, {
    credentialSecretId: returned.secretId !== '' ? returned.secretId : undefined,
    error: returned.error,
  })
  const secretId = manualSetup.savedSecret || manualSetup.selectedSecret
  const [manual, setManual] = useState(
    returned.recoverManually || (Boolean(existing?.provider_tenant_id) && !returned.secretId),
  )
  const [resuming, setResuming] = useState(Boolean(secretId))
  const [organizationOwned, setOrganizationOwned] = useState(false)
  const [organization, setOrganization] = useState('')
  const [inspected, setInspected] = useState<GitHubInstallations>()
  const [installationId, setInstallationId] = useState(returned.installationId)
  const [page, setPage] = useState(1)
  const [error, setError] = useState(returned.error)
  const [guidedBusy, setBusy] = useState(false)
  const busy = guidedBusy || manualSetup.busy
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
  const selected = inspected?.installations.find(
    (installation) => installation.id === installationId,
  )

  async function register() {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      const draft = await ensureApp()
      if (!isMounted()) return
      const request: CreateGitHubSetupRequest & { appID: string } = {
        appID: draft.id,
        expected_setup_revision: draft.setup_revision,
      }
      if (organizationOwned) request.organization = organization.trim()
      const setup = await start.mutateAsync(request)
      if (!isMounted()) return
      if (setup.app_id !== draft.id)
        throw new Error('Registration returned a different app. Please try again.')
      const form = document.createElement('form')
      form.method = 'POST'
      form.action = setup.registration_url
      const manifest = document.createElement('input')
      manifest.type = 'hidden'
      manifest.name = 'manifest'
      manifest.value = JSON.stringify(setup.manifest)
      form.append(manifest)
      document.body.append(form)
      form.submit()
      form.remove()
    } catch (cause) {
      if (isMounted()) setError(errorMessage(cause, 'Could not start GitHub registration.'))
    } finally {
      if (isMounted()) setBusy(false)
    }
  }

  async function inspectInstallations(nextPage = 1) {
    if (busy || !secretId) return
    setBusy(true)
    setError('')
    try {
      const draft = await ensureApp()
      if (!isMounted()) return
      const data = await inspect.mutateAsync({
        appID: draft.id,
        credentials_secret_ref: secretId,
        page: nextPage,
      })
      if (!isMounted()) return
      setInspected(data)
      setPage(nextPage)
      setInstallationId(
        data.installations.some((installation) => installation.id === installationId)
          ? installationId
          : '',
      )
    } catch (cause) {
      if (!isMounted()) return
      setInspected(undefined)
      setInstallationId('')
      setError(errorMessage(cause, 'Could not check GitHub installations.'))
    } finally {
      if (isMounted()) setBusy(false)
    }
  }

  async function connect() {
    if (busy || !selected || !inspected) return
    setBusy(true)
    setError('')
    try {
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
    } catch (cause) {
      if (isMounted()) setError(errorMessage(cause, 'Could not connect this GitHub installation.'))
    } finally {
      if (isMounted()) setBusy(false)
    }
  }

  if (manual)
    return (
      <div className="flex flex-col gap-4">
        {!app?.provider_tenant_id && (
          <Button
            type="button"
            variant="link"
            className="self-start px-0"
            disabled={busy}
            onClick={() => {
              setResuming(Boolean(secretId))
              setInspected(undefined)
              setInstallationId('')
              setPage(1)
              setError('')
              setManual(false)
            }}
          >
            Back to guided setup
          </Button>
        )}
        <ProjectAppSetupForm
          orgId={orgId}
          projectId={projectId}
          app={existing}
          draft={draft}
          state={manualSetup}
          appType="github_pr"
          onSaved={onConnected}
          onCancel={onCancel}
          footerAction={footerAction}
        />
      </div>
    )

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault()
        if (inspected) void connect()
        else if (resuming) void inspectInstallations()
        else void register()
      }}
    >
      <FieldGroup className="text-sm">
        <div className="flex flex-col gap-2">
          <h2 className="font-medium">Connect GitHub</h2>
          <p className="text-muted-foreground">
            {resuming
              ? 'Check your GitHub App’s installations, then connect an account.'
              : 'Create a GitHub App you own, then connect it here.'}
          </p>
        </div>
        <fieldset disabled={busy} className="flex flex-col gap-5">
          {!existing && <ProjectAppNameField name={name} onChange={setName} saved={app} />}
          {resuming ? (
            <ProjectAppCredentialPicker
              orgId={orgId}
              projectId={projectId}
              appType="github_pr"
              value={secretId}
              onChange={(id) => {
                manualSetup.setSavedSecret('')
                manualSetup.setSelectedSecret(id)
                manualSetup.setNewCredential(false)
                setInspected(undefined)
                setInstallationId('')
                setPage(1)
                setError('')
              }}
            />
          ) : (
            <>
              <Field>
                <FieldLabel htmlFor="github-owner">GitHub App owner</FieldLabel>
                <select
                  id="github-owner"
                  className={selectClass}
                  value={organizationOwned ? 'organization' : 'personal'}
                  onChange={(event) => {
                    setOrganizationOwned(event.target.value === 'organization')
                  }}
                >
                  <option value="personal">My personal account</option>
                  <option value="organization">An organization</option>
                </select>
              </Field>
              {organizationOwned && (
                <Field>
                  <FieldLabel htmlFor="github-organization">Organization login</FieldLabel>
                  <Input
                    id="github-organization"
                    value={organization}
                    required
                    maxLength={39}
                    pattern="[A-Za-z0-9]+(-[A-Za-z0-9]+)*"
                    onChange={(event) => {
                      setOrganization(event.target.value)
                    }}
                  />
                </Field>
              )}
              <FieldDescription>
                Your new GitHub App is private and installs on its owning account. To install it on
                other accounts, change its visibility in GitHub App settings.
              </FieldDescription>
            </>
          )}
          {inspected && (
            <>
              <p>
                GitHub App: <strong>{inspected.name}</strong>{' '}
                <span className="text-muted-foreground">({inspected.slug})</span>
              </p>
              {inspected.installations.length > 0 ? (
                <Field>
                  <FieldLabel htmlFor="github-installation">Installation</FieldLabel>
                  <select
                    id="github-installation"
                    className={selectClass}
                    required
                    value={installationId}
                    onChange={(event) => {
                      setInstallationId(event.target.value)
                    }}
                  >
                    <option value="">Choose an installation</option>
                    {inspected.installations.map((installation) => (
                      <option key={installation.id} value={installation.id}>
                        {installation.account}
                      </option>
                    ))}
                  </select>
                </Field>
              ) : (
                <p className="text-muted-foreground">
                  No installations on this page. Install the app in GitHub, or check again after
                  your organization approves it. Your saved credential remains available.
                </p>
              )}
              {selected && (
                <a
                  href={selected.settings_url}
                  target="_blank"
                  rel="noreferrer"
                  className="underline underline-offset-2"
                >
                  Manage repository access in GitHub
                </a>
              )}
              <div className="flex flex-wrap gap-2">
                <Button asChild variant={inspected.installations.length ? 'outline' : 'default'}>
                  <a href={inspected.install_url} target="_blank" rel="noreferrer">
                    Install in GitHub
                  </a>
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => void inspectInstallations(page)}
                >
                  Refresh installations
                </Button>
                {page > 1 && (
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => void inspectInstallations(page - 1)}
                  >
                    Previous installations
                  </Button>
                )}
                {inspected.next_page && (
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => void inspectInstallations(inspected.next_page)}
                  >
                    More installations
                  </Button>
                )}
              </div>
            </>
          )}
        </fieldset>
        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}
        <fieldset disabled={busy} className="flex flex-wrap items-center justify-end gap-2">
          {footerAction}
          {onCancel && (
            <Button type="button" variant="outline" disabled={busy} onClick={onCancel}>
              Cancel
            </Button>
          )}
          {(!inspected || inspected.installations.length > 0) && (
            <Button
              type="submit"
              loading={busy}
              disabled={busy || (resuming && !secretId) || (Boolean(inspected) && !selected)}
            >
              {inspected ? 'Connect app' : resuming ? 'Check installations' : 'Continue to GitHub'}
            </Button>
          )}
        </fieldset>
        <div className="flex flex-wrap gap-3">
          <Button
            type="button"
            variant="link"
            className="px-0"
            disabled={busy}
            onClick={() => {
              setResuming(!resuming)
              setInspected(undefined)
              setError('')
            }}
          >
            {resuming ? 'Create a new GitHub App' : 'Use a saved credential'}
          </Button>
          <Button
            type="button"
            variant="link"
            className="px-0"
            disabled={busy}
            onClick={() => {
              if (secretId) manualSetup.setNewCredential(false)
              if (inspected) manualSetup.setTenant(inspected.provider_app_id)
              if (selected) manualSetup.setAccount(selected.id)
              setManual(true)
            }}
          >
            Use an existing App
          </Button>
        </div>
      </FieldGroup>
    </form>
  )
}
