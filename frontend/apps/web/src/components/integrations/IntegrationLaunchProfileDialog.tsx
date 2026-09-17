import {
  useAgentProfileQuery,
  useAgentProfiles,
  useIntegrationLaunchProfile,
  useSetIntegrationLaunchProfile,
} from '@omnara/react'
import type {
  AgentProfile,
  GitHubActivation,
  IntegrationInstall,
  IntegrationLaunchProfile,
} from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'
import { errorMessage } from '@/lib/submit-status'

type ProfileOption = Pick<AgentProfile, 'id' | 'name'>
const activationLabels: Record<GitHubActivation, string> = {
  pr_open: 'Automatically when a PR opens',
  mention: 'When the bot is mentioned',
}
const ProfileCombobox = createResourceCombobox<ProfileOption>({
  itemKey: (profile) => profile.id,
  itemLabel: (profile) => profile.name,
  placeholder: 'Search profiles…',
})

export function IntegrationLaunchProfileDialog({
  orgId,
  projectId,
  install,
  canManage,
  onClose,
}: {
  orgId: string
  projectId: string
  install: IntegrationInstall
  canManage: boolean
  onClose: () => void
}) {
  const query = useIntegrationLaunchProfile(orgId, projectId, install.id)
  const mutation = useSetIntegrationLaunchProfile(orgId, projectId, install.id)
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !mutation.isPending) onClose()
      }}
    >
      <DialogContent showCloseButton={!mutation.isPending}>
        <DialogHeader>
          <DialogTitle>Inbound profile</DialogTitle>
          <DialogDescription>
            Choose a profile for new agents responding to incoming activity on{' '}
            {install.display_name || (install.provider_account_ref ?? 'this connection')}. Changes
            apply only to future launches. Existing conversations keep their agents and replies.
          </DialogDescription>
        </DialogHeader>
        {query.isPending ? (
          <Spinner />
        ) : query.isError ? (
          <div role="alert" className="grid gap-3">
            <p>{errorMessage(query.error, 'Could not load inbound profile')}</p>
            <Button
              variant="outline"
              onClick={() => {
                void query.refetch()
              }}
            >
              Retry
            </Button>
          </div>
        ) : (
          <LaunchProfileForm
            orgId={orgId}
            projectId={projectId}
            currentProfileId={query.data.agent_profile_id}
            currentActivation={
              install.provider === 'github'
                ? (query.data.github_activation ?? 'pr_open')
                : undefined
            }
            canManage={canManage}
            pending={mutation.isPending}
            error={mutation.error}
            onClose={onClose}
            onSave={(settings) => {
              if (!canManage || mutation.isPending) return
              mutation.mutate(settings, { onSuccess: onClose })
            }}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function LaunchProfileForm({
  orgId,
  projectId,
  currentProfileId,
  currentActivation,
  canManage,
  pending,
  error,
  onClose,
  onSave,
}: {
  orgId: string
  projectId: string
  currentProfileId: string | null
  currentActivation: GitHubActivation | undefined
  canManage: boolean
  pending: boolean
  error: Error | null
  onClose: () => void
  onSave: (settings: IntegrationLaunchProfile) => void
}) {
  const search = useTypeaheadSearch()
  const profilesQuery = useAgentProfiles(orgId, projectId, {
    enabled: canManage,
    filters: search.filters,
    sort: 'name',
  })
  const profiles = useInfiniteQueryItems(profilesQuery)
  const currentProfile = useAgentProfileQuery(orgId, projectId, currentProfileId ?? undefined)
  // An untouched selection follows refreshed server state; edits stay local until saved.
  const [draft, setDraft] = useState<ProfileOption | null | undefined>(undefined)
  const [activationDraft, setActivationDraft] = useState<GitHubActivation>()
  const activation = activationDraft ?? currentActivation
  const activationChanged = activation !== currentActivation
  const savedProfile = currentProfileId
    ? (currentProfile.data ??
      profiles.find((profile) => profile.id === currentProfileId) ?? {
        id: currentProfileId,
        name: currentProfile.isPending ? 'Loading profile…' : 'Unavailable profile',
      })
    : null
  const selected = draft === undefined ? savedProfile : draft
  const changed = (selected?.id ?? null) !== currentProfileId || activationChanged
  function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!canManage || pending || !changed) return
    const settings: IntegrationLaunchProfile = { agent_profile_id: selected?.id ?? null }
    if (activationChanged) settings.github_activation = activation
    onSave(settings)
  }
  return (
    <form onSubmit={submit}>
      <fieldset disabled={pending} className="grid gap-4">
        <Field>
          {canManage ? (
            <>
              <FieldLabel htmlFor="connection-inbound-profile">Agent profile (optional)</FieldLabel>
              <ProfileCombobox
                id="connection-inbound-profile"
                items={profiles}
                search={search}
                query={profilesQuery}
                value={selected}
                onValueChange={setDraft}
                disabled={pending}
                placeholder="New launches disabled"
              />
            </>
          ) : (
            <p className="text-sm">{savedProfile?.name ?? 'New launches disabled'}</p>
          )}
          <FieldDescription>
            Without a profile, incoming activity cannot launch new agents. Existing conversations
            can still receive replies.
          </FieldDescription>
        </Field>
        {activation && (
          <Field>
            <FieldLabel htmlFor="connection-github-activation">Start a PR agent</FieldLabel>
            {canManage ? (
              <Select
                value={activation}
                disabled={pending}
                onValueChange={(value) => {
                  if (value === 'pr_open' || value === 'mention') setActivationDraft(value)
                }}
              >
                <SelectTrigger id="connection-github-activation" className="w-full">
                  <SelectValue>{activationLabels[activation]}</SelectValue>
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="pr_open">{activationLabels.pr_open}</SelectItem>
                  <SelectItem value="mention">{activationLabels.mention}</SelectItem>
                </SelectContent>
              </Select>
            ) : (
              <p className="text-sm">{activationLabels[activation]}</p>
            )}
            <FieldDescription>
              Mentions can be in a PR description, comment, or published review. Once an agent
              starts, supported comments and commits continue that agent without another mention.
            </FieldDescription>
          </Field>
        )}
        {error && (
          <p role="alert" className="text-destructive text-sm">
            {errorMessage(error, 'Could not save inbound profile')}
          </p>
        )}
        <DialogFooter>
          <Button type="button" variant="outline" disabled={pending} onClick={onClose}>
            {canManage ? 'Cancel' : 'Done'}
          </Button>
          {canManage && (
            <Button type="submit" disabled={pending || !changed} loading={pending}>
              Save
            </Button>
          )}
        </DialogFooter>
      </fieldset>
    </form>
  )
}
