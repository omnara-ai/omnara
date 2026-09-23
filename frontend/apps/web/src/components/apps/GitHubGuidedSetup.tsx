import type { GitHubSetupInstallation, ProjectApp } from '@omnara/sdk'
import type { ReactNode } from 'react'

import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { ProjectAppNameField } from './ProjectAppNameField'
import { ProjectAppCredentialPicker } from './ProjectAppSetupCredentials'
import type { GitHubInspection, useGitHubGuidedSetup } from './useGitHubGuidedSetup'
import type { useProjectAppDraft } from './useProjectAppDraft'

const selectClass =
  'control-focus rounded-control border-input bg-card h-10 w-full border px-3 text-sm'

export function GitHubGuidedSetup({
  orgId,
  projectId,
  existing,
  draft,
  guided,
  busy,
  error,
  onUseExistingApp,
  onCancel,
  footerAction,
}: {
  orgId: string
  projectId: string
  existing?: ProjectApp
  draft: ReturnType<typeof useProjectAppDraft>
  guided: ReturnType<typeof useGitHubGuidedSetup>
  busy: boolean
  error: string
  onUseExistingApp: () => void
  onCancel?: () => void
  footerAction?: ReactNode
}) {
  const { resuming, inspection } = guided
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault()
        guided.continueSetup()
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
          {!existing && (
            <ProjectAppNameField name={draft.name} onChange={draft.setName} saved={draft.app} />
          )}
          {resuming ? (
            <ProjectAppCredentialPicker
              orgId={orgId}
              projectId={projectId}
              appType="github_pr"
              value={guided.secretId}
              onChange={guided.changeCredential}
            />
          ) : (
            <GitHubRegistrationOptions
              organizationOwned={guided.organizationOwned}
              organization={guided.organization}
              onOrganizationOwnedChange={guided.setOrganizationOwned}
              onOrganizationChange={guided.setOrganization}
            />
          )}
          {inspection && (
            <GitHubInstallationChoice
              inspection={inspection}
              selected={guided.selected}
              onSelect={guided.selectInstallation}
              onInspect={guided.inspectInstallations}
            />
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
          {(!inspection || inspection.result.installations.length > 0) && (
            <Button type="submit" loading={busy} disabled={busy || !guided.ready}>
              {inspection ? 'Connect app' : resuming ? 'Check installations' : 'Continue to GitHub'}
            </Button>
          )}
        </fieldset>
        <div className="flex flex-wrap gap-3">
          <Button
            type="button"
            variant="link"
            className="px-0"
            disabled={busy}
            onClick={guided.switchCredentialSource}
          >
            {resuming ? 'Create a new GitHub App' : 'Use a saved credential'}
          </Button>
          <Button
            type="button"
            variant="link"
            className="px-0"
            disabled={busy}
            onClick={onUseExistingApp}
          >
            Use an existing App
          </Button>
        </div>
      </FieldGroup>
    </form>
  )
}

function GitHubRegistrationOptions({
  organizationOwned,
  organization,
  onOrganizationOwnedChange,
  onOrganizationChange,
}: {
  organizationOwned: boolean
  organization: string
  onOrganizationOwnedChange: (organizationOwned: boolean) => void
  onOrganizationChange: (organization: string) => void
}) {
  return (
    <>
      <Field>
        <FieldLabel htmlFor="github-owner">GitHub App owner</FieldLabel>
        <select
          id="github-owner"
          className={selectClass}
          value={organizationOwned ? 'organization' : 'personal'}
          onChange={(event) => {
            onOrganizationOwnedChange(event.target.value === 'organization')
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
              onOrganizationChange(event.target.value)
            }}
          />
        </Field>
      )}
      <FieldDescription>
        Your new GitHub App is private and installs on its owning account. To install it on other
        accounts, change its visibility in GitHub App settings.
      </FieldDescription>
    </>
  )
}

function GitHubInstallationChoice({
  inspection: { result, page },
  selected,
  onSelect,
  onInspect,
}: {
  inspection: GitHubInspection
  selected?: GitHubSetupInstallation
  onSelect: (installationId: string) => void
  onInspect: (page: number) => void
}) {
  const nextPage = result.next_page
  return (
    <>
      <p>
        GitHub App: <strong>{result.name}</strong>{' '}
        <span className="text-muted-foreground">({result.slug})</span>
      </p>
      {result.installations.length > 0 ? (
        <Field>
          <FieldLabel htmlFor="github-installation">Installation</FieldLabel>
          <select
            id="github-installation"
            className={selectClass}
            required
            value={selected?.id ?? ''}
            onChange={(event) => {
              onSelect(event.target.value)
            }}
          >
            <option value="">Choose an installation</option>
            {result.installations.map((installation) => (
              <option key={installation.id} value={installation.id}>
                {installation.account}
              </option>
            ))}
          </select>
        </Field>
      ) : (
        <p className="text-muted-foreground">
          No installations on this page. Install the app in GitHub, or check again after your
          organization approves it. Your saved credential remains available.
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
        <Button asChild variant={result.installations.length ? 'outline' : 'default'}>
          <a href={result.install_url} target="_blank" rel="noreferrer">
            Install in GitHub
          </a>
        </Button>
        <Button
          type="button"
          variant="outline"
          onClick={() => {
            onInspect(page)
          }}
        >
          Refresh installations
        </Button>
        {page > 1 && (
          <Button
            type="button"
            variant="outline"
            onClick={() => {
              onInspect(page - 1)
            }}
          >
            Previous installations
          </Button>
        )}
        {nextPage && (
          <Button
            type="button"
            variant="outline"
            onClick={() => {
              onInspect(nextPage)
            }}
          >
            More installations
          </Button>
        )}
      </div>
    </>
  )
}
