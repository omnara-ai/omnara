import {
  useCreateIntegrationConnection,
  useCreateProjectApp,
  useCreateSecret,
  useIntegrationConnections,
  useProjectAvailableSecrets,
} from '@omnara/react'
import {
  type AgentProfile,
  type IntegrationConnection,
  profileAppDiscordKeyStatus,
  type ProfileAppProvider,
} from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { settleSubmission } from '@/lib/submit-status'

import { DeployAgentProfileDialog } from './DeployAgentProfileDialog'
import { DiscordAppInteractionsSetup, ProjectAppProfilePicker } from './ProjectAppProfilePicker'
import {
  AppCapabilityFields,
  AppConnectionField,
  AppSetupFooter,
  NewAppConnectionFields,
} from './ProjectAppSetupDialogFields'
import { submitProjectAppSetup } from './projectAppSetupSubmission'

const providerLabels = { slack: 'Slack', github: 'GitHub', discord: 'Discord' }

export function ProjectAppSetupDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  projectId: string
  profile: Pick<AgentProfile, 'id' | 'name'>
}) {
  const [provider, setProvider] = useState<ProfileAppProvider>('slack')
  const [slackOAuth, setSlackOAuth] = useState(false)
  if (slackOAuth) return <DeployAgentProfileDialog {...props} />
  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Add app</DialogTitle>
          <DialogDescription>
            {provider === 'github'
              ? `Choose a connection and when to launch ${props.profile.name}.`
              : 'Choose a connection and profiles to offer when someone mentions the bot.'}
          </DialogDescription>
        </DialogHeader>
        <div className="flex gap-2" aria-label="App provider">
          {(['slack', 'github', 'discord'] as const).map((value) => (
            <Button
              key={value}
              variant={provider === value ? 'secondary' : 'outline'}
              onClick={() => {
                setProvider(value)
              }}
            >
              {providerLabels[value]}
            </Button>
          ))}
        </div>
        <ProviderAppSetupForm
          key={provider}
          {...props}
          provider={provider}
          onNewSlack={() => {
            setSlackOAuth(true)
          }}
        />
      </DialogContent>
    </Dialog>
  )
}

function ProviderAppSetupForm({
  orgId,
  projectId,
  profile,
  provider,
  onOpenChange,
  onNewSlack,
}: {
  orgId: string
  projectId: string
  profile: Pick<AgentProfile, 'id' | 'name'>
  provider: ProfileAppProvider
  onOpenChange: (open: boolean) => void
  onNewSlack: () => void
}) {
  const connectionsQuery = useIntegrationConnections(orgId, projectId)
  const connections = useInfiniteQueryItems(connectionsQuery).filter(
    (c) => c.provider === provider && c.state === 'active',
  )
  const secretsQuery = useProjectAvailableSecrets(orgId, projectId, {
    enabled: provider !== 'slack',
    filters: { kind: provider === 'github' ? 'github_app_credentials' : 'generic' },
  })
  const createSecret = useCreateSecret(orgId)
  const createConnection = useCreateIntegrationConnection(orgId, projectId)
  const createApp = useCreateProjectApp(orgId, projectId)
  const [selection, setSelection] = useState(provider === 'slack' ? '' : 'new')
  const [newCredential, setNewCredential] = useState(true)
  const [savedSecret, setSavedSecret] = useState('')
  const [savedConnection, setSavedConnection] = useState<IntegrationConnection>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [profiles, setProfiles] = useState<Pick<AgentProfile, 'id' | 'name'>[]>([profile])
  const [interactions, setInteractions] = useState(provider === 'slack')
  const connection = savedConnection ?? connections.find((c) => c.id === selection)
  const creating = selection === 'new' && !savedConnection
  const discordKeyInput = {
    definition: `omnara.${provider}`,
    slots: profiles.map((item) => ({ agent_profile_id: item.id })),
    interactions,
    providerConfig: connection?.provider_config,
  }
  const discordKey = profileAppDiscordKeyStatus(discordKeyInput)
  const missingSavedKey = !creating && discordKey.missing

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy) return
    const form = new FormData(event.currentTarget)
    setError('')
    setBusy(true)
    const result = await settleSubmission(() =>
      submitProjectAppSetup(
        {
          form,
          projectId,
          provider,
          profileId: profile.id,
          profileIds: profiles.map((item) => item.id),
          interactions,
          connection,
          creating,
          savedSecret,
          newCredential,
        },
        {
          createSecret: createSecret.mutateAsync,
          createConnection: createConnection.mutateAsync,
          createApp: createApp.mutateAsync,
          onSecretSaved: setSavedSecret,
          onConnectionSaved: setSavedConnection,
        },
      ),
    )
    setBusy(false)
    if (result.ok) onOpenChange(false)
    else
      setError(result.error instanceof Error ? result.error.message : 'Could not save app setup.')
  }

  return (
    <form onSubmit={(event) => void submit(event)} autoComplete="off">
      <FieldGroup>
        <fieldset disabled={busy} className="flex flex-col gap-4">
          <AppConnectionField
            connectionsQuery={connectionsQuery}
            connections={connections}
            provider={provider}
            providerLabel={providerLabels[provider]}
            profileName={profile.name}
            selection={selection}
            savedConnection={savedConnection}
            locked={savedConnection !== undefined || savedSecret !== ''}
            onSelectionChange={setSelection}
            onNewSlack={onNewSlack}
          />
          {provider !== 'github' && (
            <ProjectAppProfilePicker
              orgId={orgId}
              projectId={projectId}
              value={profiles}
              onChange={setProfiles}
              disabled={busy}
            />
          )}
          <Field>
            <FieldLabel htmlFor="app-name">App setup name</FieldLabel>
            <Input
              id="app-name"
              name="name"
              required
              maxLength={64}
              defaultValue={`${profile.name.slice(0, 45)} ${providerLabels[provider]}`}
            />
          </Field>
          {creating && (
            <NewAppConnectionFields
              provider={provider}
              providerLabel={providerLabels[provider]}
              profileName={profile.name}
              savedSecret={savedSecret}
              newCredential={newCredential}
              secretsQuery={secretsQuery}
              onDifferentCredentials={() => {
                setSavedSecret('')
                setNewCredential(false)
              }}
              onNewCredentialChange={setNewCredential}
              keyRequired={discordKey.required}
              keyPattern={discordKey.pattern}
            />
          )}
          <AppCapabilityFields
            provider={provider}
            providerLabel={providerLabels[provider]}
            interactions={interactions}
            onInteractionsChange={setInteractions}
          />
          {provider === 'discord' && <DiscordAppInteractionsSetup connection={connection} />}
          {missingSavedKey && (
            <p role="alert">
              Save a valid public_key on this Discord connection before enabling multiple choices or
              agent questions.
            </p>
          )}
        </fieldset>
        <AppSetupFooter
          busy={busy}
          error={error}
          savedConnection={savedConnection}
          connectionReady={creating || Boolean(connection)}
          missingSavedKey={missingSavedKey}
          provider={provider}
          profileCount={profiles.length}
        />
      </FieldGroup>
    </form>
  )
}
