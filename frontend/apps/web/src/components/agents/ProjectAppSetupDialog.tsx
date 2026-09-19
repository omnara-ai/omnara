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
  profileAppSetup,
  profileAppTools,
} from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'
import * as z from 'zod'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

import { DeployAgentProfileDialog } from './DeployAgentProfileDialog'
import { DiscordAppInteractionsSetup, ProjectAppProfilePicker } from './ProjectAppProfilePicker'
import { AppCredentialFields } from './ProjectAppSetupDialogCredentials'

const providerLabels = { slack: 'Slack', github: 'GitHub', discord: 'Discord' }
const selectClass = 'border-input bg-background h-9 w-full rounded-md border px-3 text-sm'

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
  const secrets = useInfiniteQueryItems(secretsQuery).map((access) => access.secret)
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
    const value = (key: string) =>
      z
        .string()
        .parse(form.get(key) ?? '')
        .trim()
    setError('')
    setBusy(true)
    try {
      if (provider !== 'github' && (profiles.length === 0 || profiles.length > 16)) {
        throw new Error('Choose between 1 and 16 profiles.')
      }
      const keyConfig = creating ? { public_key: value('publicKey') } : connection?.provider_config
      if (profileAppDiscordKeyStatus({ ...discordKeyInput, providerConfig: keyConfig }).missing) {
        throw new Error(
          'Save a valid public_key on the Discord connection to enable multiple choices or agent questions.',
        )
      }
      let selected = connection
      if (creating) {
        let secretId = savedSecret || value('secret')
        if (!secretId && newCredential) {
          const secret = await createSecret.mutateAsync({
            owner: { kind: 'project', project_id: projectId },
            name: value('secretName'),
            material:
              provider === 'github'
                ? {
                    kind: 'github_app_credentials',
                    app_id: value('tenant'),
                    private_key: value('privateKey'),
                    webhook_secret: value('webhookSecret'),
                  }
                : { kind: 'generic', value: value('botToken') },
          })
          secretId = secret.id
          setSavedSecret(secret.id)
        }
        const providerConfig: Record<string, string | number> = {}
        if (provider === 'discord') {
          providerConfig.shard_count = Number(value('shards'))
          if (value('publicKey')) providerConfig.public_key = value('publicKey')
        }
        selected = await createConnection.mutateAsync({
          provider,
          provider_tenant_id: value('tenant'),
          provider_account_ref: value('account'),
          provider_agent_display_name: value('name'),
          credential_secret_id: secretId,
          provider_config: providerConfig,
        })
        setSavedConnection(selected)
      }
      if (selected?.state !== 'active') throw new Error('Choose an active connection.')
      await createApp.mutateAsync(
        profileAppSetup({
          provider,
          name: value('name'),
          connectionId: selected.id,
          profileId: profile.id,
          profileIds: provider === 'github' ? undefined : profiles.map((item) => item.id),
          scopeRef: provider === 'slack' ? selected.provider_tenant_id : value('scope'),
          tools: z.array(z.string()).parse(form.getAll('tools')),
          listen: form.has('listen'),
          interactions,
        }),
      )
      onOpenChange(false)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not save app setup.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={(event) => void submit(event)} autoComplete="off">
      <FieldGroup>
        <fieldset disabled={busy} className="flex flex-col gap-4">
          <Field>
            <FieldLabel htmlFor="app-connection">Connection</FieldLabel>
            <select
              id="app-connection"
              className={selectClass}
              value={savedConnection?.id ?? selection}
              disabled={savedConnection !== undefined || savedSecret !== ''}
              onChange={(e) => {
                setSelection(e.target.value)
              }}
              required
            >
              <option value="">Choose an active connection</option>
              {provider !== 'slack' && (
                <option value="new">New {providerLabels[provider]} connection</option>
              )}
              {savedConnection && !connections.some((c) => c.id === savedConnection.id) && (
                <option value={savedConnection.id}>
                  {savedConnection.provider_agent_display_name || savedConnection.id}
                </option>
              )}
              {connections.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.provider_agent_display_name || c.id} · {c.provider_tenant_id} /{' '}
                  {c.provider_account_ref}
                </option>
              ))}
            </select>
            {connectionsQuery.isError && (
              <p role="alert">
                Could not load connections.{' '}
                <button type="button" onClick={() => void connectionsQuery.refetch()}>
                  Retry
                </button>
              </p>
            )}
            {connectionsQuery.hasNextPage && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={connectionsQuery.isFetchingNextPage}
                onClick={() => void connectionsQuery.fetchNextPage()}
              >
                More connections
              </Button>
            )}
            {provider === 'slack' && (
              <>
                <Button type="button" variant="outline" onClick={onNewSlack}>
                  Connect a Slack app through OAuth
                </Button>
                <FieldDescription>
                  OAuth starts with {profile.name}. After setup, use Edit profiles to add more
                  profiles to the same Slack app.
                </FieldDescription>
              </>
            )}
            <FieldDescription>
              Connections are shared across project apps. Reuse the same provider account here.
            </FieldDescription>
          </Field>
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
            <>
              <Field>
                <FieldLabel htmlFor="provider-tenant">
                  {provider === 'github' ? 'GitHub App ID' : 'Discord Application ID'}
                </FieldLabel>
                <Input
                  id="provider-tenant"
                  name="tenant"
                  required
                  pattern="[1-9][0-9]*"
                  readOnly={Boolean(savedSecret)}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="provider-account">
                  {provider === 'github' ? 'GitHub Installation ID' : 'Discord bot User ID'}
                </FieldLabel>
                <Input id="provider-account" name="account" required pattern="[1-9][0-9]*" />
                <FieldDescription>
                  {provider === 'github'
                    ? 'Use your own GitHub App installation. The credential App ID must match.'
                    : 'Use the bot’s user ID, not a server (guild) ID.'}
                </FieldDescription>
              </Field>
              {savedSecret ? (
                <p className="text-sm">
                  Credentials saved as {savedSecret}. Retry uses this secret; closing keeps it.
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      setSavedSecret('')
                      setNewCredential(false)
                    }}
                  >
                    Choose different credentials
                  </Button>
                </p>
              ) : (
                <>
                  <label className="flex gap-2 text-sm">
                    <input
                      type="checkbox"
                      checked={newCredential}
                      onChange={(e) => {
                        setNewCredential(e.target.checked)
                      }}
                    />
                    Store new credentials
                  </label>
                  {newCredential ? (
                    <>
                      <Field>
                        <FieldLabel htmlFor="credential-name">Credential name</FieldLabel>
                        <Input
                          id="credential-name"
                          name="secretName"
                          required
                          maxLength={64}
                          defaultValue={`${providerLabels[provider]} ${profile.name.slice(0, 35)} credentials`}
                        />
                      </Field>
                      <AppCredentialFields provider={provider} />
                    </>
                  ) : (
                    <Field>
                      <FieldLabel htmlFor="existing-secret">Existing project credential</FieldLabel>
                      <select id="existing-secret" className={selectClass} name="secret" required>
                        <option value="">Choose a secret</option>
                        {secrets.map((s) => (
                          <option key={s.id} value={s.id}>
                            {s.name}
                          </option>
                        ))}
                      </select>
                      {secretsQuery.isError && (
                        <p role="alert">
                          Could not load credentials.{' '}
                          <button type="button" onClick={() => void secretsQuery.refetch()}>
                            Retry
                          </button>
                        </p>
                      )}
                      {secretsQuery.hasNextPage && (
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          onClick={() => void secretsQuery.fetchNextPage()}
                        >
                          More credentials
                        </Button>
                      )}
                    </Field>
                  )}
                </>
              )}
              {provider === 'discord' && (
                <>
                  <Field>
                    <FieldLabel htmlFor="discord-public-key">
                      Interaction public key{discordKey.required ? ' (required)' : ' (optional)'}
                    </FieldLabel>
                    <Input
                      id="discord-public-key"
                      name="publicKey"
                      pattern={discordKey.pattern}
                      required={discordKey.required}
                    />
                  </Field>
                  <Field>
                    <FieldLabel htmlFor="discord-shards">Gateway shard count</FieldLabel>
                    <Input
                      id="discord-shards"
                      name="shards"
                      type="number"
                      min={1}
                      max={4096}
                      required
                      defaultValue={1}
                    />
                    <FieldDescription>
                      Usually 1. Increase this if Discord requires more shards for your bot.
                    </FieldDescription>
                  </Field>
                </>
              )}
            </>
          )}
          {provider !== 'slack' && (
            <Field>
              <FieldLabel htmlFor="launcher-scope">
                {provider === 'github' ? 'Repository ID' : 'Channel ID'}
              </FieldLabel>
              <Input id="launcher-scope" name="scope" pattern="[1-9][0-9]*" required />
              <FieldDescription>
                {provider === 'github'
                  ? 'Launch on new pull requests in this repository. Use its numeric ID, not owner/repository.'
                  : 'Launch on mentions in this channel. Give the bot access to it.'}
              </FieldDescription>
            </Field>
          )}
          <Field>
            <FieldLabel>Agent tools</FieldLabel>
            {profileAppTools[provider].map((tool) => (
              <label key={tool} className="flex gap-2 text-sm">
                <input type="checkbox" name="tools" value={tool} defaultChecked />
                {tool.replaceAll('_', ' ')}
              </label>
            ))}
            <FieldDescription>
              The profile’s explicit tool permissions still apply.
            </FieldDescription>
          </Field>
          <label className="flex gap-2 text-sm">
            <input type="checkbox" name="listen" defaultChecked />
            Receive later messages{provider === 'github' ? ' and commits' : ''} in the launched
            conversation
          </label>
          {provider !== 'github' && (
            <label className="flex gap-2 text-sm">
              <input
                type="checkbox"
                name="interactions"
                checked={interactions}
                onChange={(event) => {
                  setInteractions(event.target.checked)
                }}
              />
              Show agent questions and approvals in {providerLabels[provider]}
            </label>
          )}
          {provider === 'discord' && <DiscordAppInteractionsSetup connection={connection} />}
          {missingSavedKey && (
            <p role="alert">
              Save a valid public_key on this Discord connection before enabling multiple choices or
              agent questions.
            </p>
          )}
        </fieldset>
        {savedConnection && (
          <p className="text-sm">
            Connection saved: {savedConnection.id}. Retry creates only the app setup. Closing keeps
            the connection.
          </p>
        )}
        {error && (
          <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
            {error}
          </p>
        )}
        <DialogFooter>
          <Button
            type="submit"
            loading={busy}
            disabled={
              busy ||
              missingSavedKey ||
              (!creating && !connection) ||
              (provider !== 'github' && (profiles.length === 0 || profiles.length > 16))
            }
          >
            Save app setup
          </Button>
        </DialogFooter>
      </FieldGroup>
    </form>
  )
}
