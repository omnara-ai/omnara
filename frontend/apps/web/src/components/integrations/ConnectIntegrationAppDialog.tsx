import { useAgentProfiles, useStartIntegrationConnection } from '@omnara/react'
import type {
  AgentProfile,
  IntegrationAppSummary,
  StartIntegrationConnectionRequest,
} from '@omnara/sdk'
import { type SyntheticEvent, useEffect, useRef, useState } from 'react'

import { integrationProviderLabel } from '@/components/org/integrationCredentials'
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
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'
import { errorMessage } from '@/lib/submit-status'

import { integrationAuthorizationUrl } from './integrationOAuth'

const ProfileCombobox = createResourceCombobox<AgentProfile>({
  itemKey: (profile) => profile.id,
  itemLabel: (profile) => profile.name,
  placeholder: 'Search profiles…',
})

export function ConnectIntegrationAppDialog({
  orgId,
  projectId,
  app,
  onClose,
}: {
  orgId: string
  projectId: string
  app: IntegrationAppSummary
  onClose: () => void
}) {
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const search = useTypeaheadSearch()
  const profilesQuery = useAgentProfiles(orgId, projectId, {
    filters: search.filters,
    sort: 'name',
  })
  const profiles = useInfiniteQueryItems(profilesQuery)
  const [profile, setProfile] = useState<AgentProfile | null>(null)
  const [error, setError] = useState('')
  const [authorizationUrl, setAuthorizationUrl] = useState<string | null>(null)
  const mutation = useStartIntegrationConnection(orgId, projectId)
  const pending = mutation.isPending || authorizationUrl !== null
  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending) return
    setError('')
    try {
      const body: StartIntegrationConnectionRequest = {
        integration_app_id: app.id,
        return_to: `/projects/${projectId}/integrations`,
      }
      if (profile) body.agent_profile_id = profile.id
      const setup = await mutation.mutateAsync(body)
      if (!mounted.current) return
      const url = integrationAuthorizationUrl(setup.oauth_url)
      if (!url) {
        setError('Could not open the app authorization page. Try connecting again.')
        return
      }
      setAuthorizationUrl(url)
      window.location.assign(url)
    } catch (err) {
      setAuthorizationUrl(null)
      setError(errorMessage(err, 'Could not start connection'))
    }
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !mutation.isPending) onClose()
      }}
    >
      <DialogContent showCloseButton={!mutation.isPending}>
        <DialogHeader>
          <DialogTitle>Connect {app.name || integrationProviderLabel(app.provider)}</DialogTitle>
          <DialogDescription>
            Authorize access in {integrationProviderLabel(app.provider)} to connect this project.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={(event) => void submit(event)}>
          <fieldset disabled={pending} className="grid gap-4">
            <Field>
              <FieldLabel htmlFor="inbound-profile">Inbound agent profile (optional)</FieldLabel>
              <ProfileCombobox
                id="inbound-profile"
                items={profiles}
                search={search}
                query={profilesQuery}
                value={profile}
                onValueChange={setProfile}
                disabled={pending}
                placeholder="No inbound profile"
              />
              <FieldDescription>
                Choose a profile for agents responding to incoming activity. You can connect without
                one.
              </FieldDescription>
            </Field>
            {error && (
              <p role="alert" className="text-destructive text-sm">
                {error}
              </p>
            )}
          </fieldset>
          <DialogFooter className="mt-4">
            <Button type="button" variant="outline" disabled={mutation.isPending} onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending} loading={pending}>
              Continue to {integrationProviderLabel(app.provider)}
            </Button>
          </DialogFooter>
        </form>
        {authorizationUrl && (
          <p className="text-muted-foreground text-sm">
            If the authorization page did not open,{' '}
            <a href={authorizationUrl} className="text-foreground underline underline-offset-2">
              continue to {integrationProviderLabel(app.provider)}
            </a>
            .
          </p>
        )}
      </DialogContent>
    </Dialog>
  )
}
