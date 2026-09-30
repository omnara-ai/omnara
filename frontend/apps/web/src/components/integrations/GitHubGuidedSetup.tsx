import type { GitHubSetupInstallation, Integration } from '@omnara/sdk'
import type { ReactNode } from 'react'

import { Button } from '@/components/ui/button'
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
  FieldSeparator,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { IntegrationNameField } from './IntegrationNameField'
import { IntegrationCredentialPicker } from './IntegrationSetupCredentials'
import type { GitHubInspection, useGitHubGuidedSetup } from './useGitHubGuidedSetup'
import type { useIntegrationDraft } from './useIntegrationDraft'

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
  disabled = false,
}: {
  orgId: string
  projectId: string
  existing?: Integration
  draft: ReturnType<typeof useIntegrationDraft>
  guided: ReturnType<typeof useGitHubGuidedSetup>
  busy: boolean
  error: string
  onUseExistingApp: () => void
  onCancel?: () => void
  footerAction?: ReactNode
  disabled?: boolean
}) {
  const { resuming, inspection } = guided
  const locked = busy || disabled
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault()
        guided.continueSetup()
      }}
    >
      <FieldGroup className="text-sm">
        <div className="flex flex-col gap-2">
          <h2 className="font-medium">
            {resuming ? 'Connect a saved GitHub App' : 'Create a GitHub App'}
          </h2>
          <p className="text-muted-foreground">
            {resuming
              ? 'Select a saved credential below, then click Check installations and choose an installation to connect.'
              : 'Fill in the details below, then click Continue to GitHub to create your App. You’ll return here to connect it.'}
          </p>
        </div>
        <fieldset disabled={busy} className="flex flex-col gap-5">
          {!existing && (
            <IntegrationNameField
              name={draft.name}
              onChange={draft.setName}
              saved={draft.integration}
            />
          )}
          {resuming ? (
            <IntegrationCredentialPicker
              orgId={orgId}
              projectId={projectId}
              integrationKind="github_pr"
              value={guided.secretId}
              onChange={guided.changeCredential}
              disabled={locked}
            />
          ) : (
            <GitHubRegistrationOptions
              organizationOwned={guided.organizationOwned}
              organization={guided.organization}
              onOrganizationOwnedChange={guided.setOrganizationOwned}
              onOrganizationChange={guided.setOrganization}
              disabled={locked}
            />
          )}
          {inspection && (
            <GitHubInstallationChoice
              inspection={inspection}
              selected={guided.selected}
              onSelect={guided.selectInstallation}
              onInspect={guided.inspectInstallations}
              disabled={locked}
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
              {inspection
                ? 'Connect integration'
                : resuming
                  ? 'Check installations'
                  : 'Continue to GitHub'}
            </Button>
          )}
        </fieldset>
        <FieldSeparator>OR</FieldSeparator>
        <div className="flex flex-col gap-6">
          <div className="flex flex-col gap-2">
            <h2 className="font-medium">
              {resuming ? 'Other setup options' : 'Use an existing GitHub App'}
            </h2>
            <p className="text-muted-foreground">
              {resuming
                ? 'Create a new App instead, or copy an App’s details from GitHub.'
                : 'Choose a credential saved in Omnara, or copy the App’s details from GitHub.'}
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={guided.switchCredentialSource}
            >
              {resuming ? 'Create a new GitHub App' : 'Use a saved credential'}
            </Button>
            <Button type="button" variant="outline" disabled={busy} onClick={onUseExistingApp}>
              Enter App details
            </Button>
          </div>
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
  disabled,
}: {
  organizationOwned: boolean
  organization: string
  onOrganizationOwnedChange: (organizationOwned: boolean) => void
  onOrganizationChange: (organization: string) => void
  disabled: boolean
}) {
  return (
    <>
      <Field>
        <FieldLabel htmlFor="github-owner">GitHub App owner</FieldLabel>
        <Select
          value={organizationOwned ? 'organization' : 'personal'}
          disabled={disabled}
          onValueChange={(value) => {
            onOrganizationOwnedChange(value === 'organization')
          }}
        >
          <SelectTrigger id="github-owner" className="w-full">
            <SelectValue>{organizationOwned ? 'Organization' : 'Personal account'}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="personal" disabled={disabled}>
              Personal account
            </SelectItem>
            <SelectItem value="organization" disabled={disabled}>
              Organization
            </SelectItem>
          </SelectContent>
        </Select>
        <FieldDescription>
          Choose the account or organization that owns the repositories you want to review.
        </FieldDescription>
      </Field>
      {organizationOwned && (
        <Field>
          <FieldLabel htmlFor="github-organization">Organization name</FieldLabel>
          <Input
            id="github-organization"
            value={organization}
            required
            maxLength={39}
            pattern="[A-Za-z0-9]+(-[A-Za-z0-9]+)*"
            placeholder="acme"
            onChange={(event) => {
              onOrganizationChange(event.target.value)
            }}
          />
          <FieldDescription>
            Enter just the name from its GitHub URL, like acme for github.com/acme.
          </FieldDescription>
        </Field>
      )}
    </>
  )
}

function GitHubInstallationChoice({
  inspection: { result, page },
  selected,
  onSelect,
  onInspect,
  disabled,
}: {
  inspection: GitHubInspection
  selected?: GitHubSetupInstallation
  onSelect: (installationId: string) => void
  onInspect: (page: number) => void
  disabled: boolean
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
          <Select value={selected?.id ?? ''} required disabled={disabled} onValueChange={onSelect}>
            <SelectTrigger id="github-installation" className="w-full">
              <SelectValue placeholder="Choose an installation">{selected?.account}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {result.installations.map((installation) => (
                <SelectItem key={installation.id} value={installation.id} disabled={disabled}>
                  {installation.account}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
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
