import { useIntegrationConnection, useUpdateProjectApp } from '@omnara/react'
import {
  type AgentProfile,
  profileAppDiscordKeyStatus,
  profileAppProfileUpdate,
  type ProjectApp,
} from '@omnara/sdk'
import { type SyntheticEvent, useMemo, useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

import { DiscordAppInteractionsSetup, ProjectAppProfilePicker } from './ProjectAppProfilePicker'

export function ProjectAppProfilesDialog({
  orgId,
  projectId,
  app,
  onOpenChange,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
  onOpenChange: (open: boolean) => void
}) {
  const slots = app.settings.launcher?.slots ?? []
  const [profiles, setProfiles] = useState<Pick<AgentProfile, 'id' | 'name'>[]>(() =>
    [
      ...new Set(
        slots.flatMap((slot) =>
          !slot.agent_id && slot.agent_profile_id ? [slot.agent_profile_id] : [],
        ),
      ),
    ].map((id) => ({ id, name: id })),
  )
  const [error, setError] = useState('')
  const update = useUpdateProjectApp(orgId, projectId)
  const connection = useIntegrationConnection(
    orgId,
    projectId,
    app.settings.resource.connection ?? '',
  )
  const retainedSlots = slots.filter((slot) => Boolean(slot.agent_id) || !slot.agent_profile_id)
  const validation = useMemo(() => {
    try {
      return {
        request: profileAppProfileUpdate(
          app,
          profiles.map((profile) => profile.id),
        ),
        error: '',
      }
    } catch (cause) {
      return {
        request: undefined,
        error: cause instanceof Error ? cause.message : 'Could not update offered profiles.',
      }
    }
  }, [app, profiles])
  const nextSlots = validation.request?.settings.launcher?.slots ?? []
  const discordKey = profileAppDiscordKeyStatus({
    definition: app.settings.resource.definition,
    slots: nextSlots,
    interactions: Boolean(app.settings.resource.interaction_handler),
    providerConfig: connection.data?.provider_config,
  })

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (update.isPending || discordKey.missing || !validation.request) return
    setError('')
    try {
      await update.mutateAsync({
        appID: app.id,
        ...validation.request,
      })
      onOpenChange(false)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not save offered profiles.')
    }
  }

  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Edit offered profiles</DialogTitle>
          <DialogDescription>
            Update {app.name} using its existing connection. Other saved app settings and existing
            agents are kept. Changes apply to future mentions.
          </DialogDescription>
        </DialogHeader>
        <form className="flex flex-col gap-4" onSubmit={(event) => void submit(event)}>
          <ProjectAppProfilePicker
            orgId={orgId}
            projectId={projectId}
            value={profiles}
            onChange={setProfiles}
            slotCount={validation.request ? nextSlots.length : null}
            disabled={update.isPending}
          />
          {retainedSlots.length > 0 && (
            <p className="text-muted-foreground text-sm">
              This setup also has {retainedSlots.length} existing-agent or other slots. They are
              kept unchanged and count toward the 16-slot limit.
            </p>
          )}
          {validation.error && <p role="alert">{validation.error}</p>}
          {discordKey.required && <DiscordAppInteractionsSetup connection={connection.data} />}
          {discordKey.missing && (
            <p role="alert">
              {connection.isPending
                ? 'Checking Discord public key…'
                : 'A valid public_key must be saved on this Discord connection before saving multiple choices or agent questions.'}
              {connection.isError && (
                <Button type="button" variant="link" onClick={() => void connection.refetch()}>
                  Retry connection
                </Button>
              )}
            </p>
          )}
          {error && (
            <p role="alert" className="text-destructive text-sm">
              {error}
            </p>
          )}
          <DialogFooter>
            <Button
              type="submit"
              loading={update.isPending}
              disabled={update.isPending || discordKey.missing || !validation.request}
            >
              Save profiles
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
