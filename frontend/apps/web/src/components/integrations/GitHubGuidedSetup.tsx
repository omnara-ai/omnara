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
import type {
  GitHubInspection,
  GitHubSetupAction,
  useGitHubGuidedSetup,
} from './useGitHubGuidedSetup'
import type { useIntegrationDraft } from './useIntegrationDraft'

const submitLabels: Record<GitHubSetupAction, string> = {
  register: 'Continue to GitHub',
  inspect: 'Check GitHub access',
  connect: 'Connect integration',
}

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
  const { resuming, returned, inspection } = guided
  const locked = busy || disabled
  const showSubmit = inspection ? inspection.result.installations.length > 0 : !returned
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault()
        guided.continueSetup()
      }}
    >
      <FieldGroup className="text-sm">
        <GitHubSetupIntroduction returned={returned} resuming={resuming} inspection={inspection} />
        <fieldset disabled={busy} className="flex flex-col gap-5">
          {!existing && (
            <IntegrationNameField
              name={draft.name}
              onChange={draft.setName}
              saved={draft.integration}
            />
          )}
          {returned ? (
            !inspection && (
              <p role="status" className="text-muted-foreground">
                Checking GitHub access…
              </p>
            )
          ) : resuming ? (
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
              returnsToOmnara={returned}
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
          {!returned && footerAction}
          {onCancel && (
            <Button type="button" variant="outline" disabled={busy} onClick={onCancel}>
              Cancel
            </Button>
          )}
          {showSubmit && (
            <Button type="submit" loading={busy} disabled={busy || !guided.ready}>
              {submitLabels[guided.nextAction]}
            </Button>
          )}
        </fieldset>
        {!returned && (
          <GitHubSetupAlternatives
            resuming={resuming}
            disabled={busy}
            onSwitchCredentialSource={guided.switchCredentialSource}
            onUseExistingApp={onUseExistingApp}
          />
        )}
      </FieldGroup>
    </form>
  )
}

function GitHubSetupIntroduction({
  returned,
  resuming,
  inspection,
}: {
  returned: boolean
  resuming: boolean
  inspection?: GitHubInspection
}) {
  return (
    <div className="flex flex-col gap-2">
      <h2 className="font-medium">
        {returned
          ? 'Connect your GitHub App'
          : resuming
            ? 'Connect a saved GitHub App'
            : 'Create a GitHub App'}
      </h2>
      <p className="text-muted-foreground">
        {returned
          ? inspection?.result.installations.length
            ? 'Connect a GitHub account below to finish setup.'
            : 'Choose which repositories your App can access on GitHub.'
          : resuming
            ? 'Select a saved credential, then check which GitHub accounts it can access.'
            : 'Fill in the details below, then click Continue to GitHub to create your App. You’ll return here to connect it.'}
      </p>
    </div>
  )
}

function GitHubSetupAlternatives({
  resuming,
  disabled,
  onSwitchCredentialSource,
  onUseExistingApp,
}: {
  resuming: boolean
  disabled: boolean
  onSwitchCredentialSource: () => void
  onUseExistingApp: () => void
}) {
  return (
    <>
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
            disabled={disabled}
            onClick={onSwitchCredentialSource}
          >
            {resuming ? 'Create a new GitHub App' : 'Use a saved credential'}
          </Button>
          <Button type="button" variant="outline" disabled={disabled} onClick={onUseExistingApp}>
            Enter App details
          </Button>
        </div>
      </div>
    </>
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
  inspection,
  returnsToOmnara,
  selected,
  onSelect,
  onInspect,
  disabled,
}: {
  inspection: GitHubInspection
  returnsToOmnara: boolean
  selected?: GitHubSetupInstallation
  onSelect: (installationId: string) => void
  onInspect: (page: number) => void
  disabled: boolean
}) {
  const { result } = inspection
  return (
    <>
      <p>
        GitHub App: <strong>{result.name}</strong>{' '}
        <span className="text-muted-foreground">({result.slug})</span>
      </p>
      <GitHubAccountSelection
        inspection={inspection}
        returnsToOmnara={returnsToOmnara}
        selected={selected}
        onSelect={onSelect}
        disabled={disabled}
      />
      <GitHubInstallationActions
        inspection={inspection}
        returnsToOmnara={returnsToOmnara}
        selected={selected}
        onInspect={onInspect}
      />
    </>
  )
}

function GitHubAccountSelection({
  inspection: { result, page },
  returnsToOmnara,
  selected,
  onSelect,
  disabled,
}: {
  inspection: GitHubInspection
  returnsToOmnara: boolean
  selected?: GitHubSetupInstallation
  onSelect: (installationId: string) => void
  disabled: boolean
}) {
  const accounts = result.installations.length
  const sole = page === 1 && !result.next_page && accounts === 1
  return (
    <>
      {accounts === 0 ? (
        <p className="text-muted-foreground">
          {page === 1
            ? returnsToOmnara
              ? 'The App can’t access any repositories yet. Choose repositories on GitHub, then you’ll return here automatically. If an organization owner must approve it, refresh accounts once they have.'
              : 'The App can’t access any repositories yet. Choose repositories on GitHub, then return here and refresh accounts. If an organization owner must approve it, refresh once they have.'
            : 'No more accounts.'}
        </p>
      ) : sole && selected ? (
        <p>
          GitHub account: <strong>{selected.account}</strong>
        </p>
      ) : (
        <Field>
          <FieldLabel htmlFor="github-installation">GitHub account</FieldLabel>
          <Select value={selected?.id ?? ''} required disabled={disabled} onValueChange={onSelect}>
            <SelectTrigger id="github-installation" className="w-full">
              <SelectValue placeholder="Choose an account">{selected?.account}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {result.installations.map((installation) => (
                <SelectItem key={installation.id} value={installation.id} disabled={disabled}>
                  {installation.account}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <FieldDescription>
            The App can access repositories on these accounts. Choose the one agents should review.
          </FieldDescription>
        </Field>
      )}
    </>
  )
}

function GitHubInstallationActions({
  inspection: { result, page },
  returnsToOmnara,
  selected,
  onInspect,
}: {
  inspection: GitHubInspection
  returnsToOmnara: boolean
  selected?: GitHubSetupInstallation
  onInspect: (page: number) => void
}) {
  const nextPage = result.next_page
  const accounts = result.installations.length
  return (
    <>
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
        <Button asChild variant={accounts ? 'outline' : 'default'}>
          <a
            href={result.install_url}
            target={returnsToOmnara && page === 1 && accounts === 0 ? undefined : '_blank'}
            rel="noreferrer"
          >
            {accounts ? 'Add an account on GitHub' : 'Choose repositories on GitHub'}
          </a>
        </Button>
        <Button
          type="button"
          variant="outline"
          onClick={() => {
            onInspect(page)
          }}
        >
          Refresh accounts
        </Button>
        {page > 1 && (
          <Button
            type="button"
            variant="outline"
            onClick={() => {
              onInspect(page - 1)
            }}
          >
            Previous accounts
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
            More accounts
          </Button>
        )}
      </div>
    </>
  )
}
