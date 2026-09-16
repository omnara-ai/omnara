import {
  useAgentProfileQuery,
  useAgentProfiles,
  useIntegrationLaunchProfile,
  useSetIntegrationLaunchProfile,
} from '@omnara/react'
import type { AgentProfile, IntegrationInstall } from '@omnara/sdk'
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
import { Spinner } from '@/components/ui/spinner'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'
import { errorMessage } from '@/lib/submit-status'

type ProfileOption = Pick<AgentProfile, 'id' | 'name'>
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
            canManage={canManage}
            pending={mutation.isPending}
            error={mutation.error}
            onClose={onClose}
            onSave={(profileId) => {
              if (!canManage || mutation.isPending) return
              mutation.mutate({ agent_profile_id: profileId }, { onSuccess: onClose })
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
  canManage,
  pending,
  error,
  onClose,
  onSave,
}: {
  orgId: string
  projectId: string
  currentProfileId: string | null
  canManage: boolean
  pending: boolean
  error: Error | null
  onClose: () => void
  onSave: (profileId: string | null) => void
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
  const savedProfile = currentProfileId
    ? (currentProfile.data ??
      profiles.find((profile) => profile.id === currentProfileId) ?? {
        id: currentProfileId,
        name: currentProfile.isPending ? 'Loading profile…' : 'Unavailable profile',
      })
    : null
  const selected = draft === undefined ? savedProfile : draft
  const changed = (selected?.id ?? null) !== currentProfileId
  function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!canManage || pending || !changed) return
    onSave(selected?.id ?? null)
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
