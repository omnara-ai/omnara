import type { useIntegrationConnections, useProjectAvailableSecrets } from '@omnara/react'
import { type IntegrationConnection, type ProfileAppProvider, type ProjectApp } from '@omnara/sdk'

import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

import { AppCredentialFields } from './ProjectAppFormCredentials'
import type { useProjectAppFormConnection } from './useProjectAppFormConnection'

const selectClass = 'border-input bg-background h-9 w-full rounded-md border px-3 text-sm'

export function ProjectAppConnectionPicker({
  app,
  provider,
  providerLabel,
  lookup,
  savedConnection,
  locked,
  onConnectSlack,
}: {
  app?: ProjectApp
  provider: ProfileAppProvider
  providerLabel: string
  lookup: ReturnType<typeof useProjectAppFormConnection>
  savedConnection?: IntegrationConnection
  locked: boolean
  onConnectSlack?: () => void
}) {
  const { connection, connectionQuery } = lookup
  return (
    <>
      {app ? (
        <Field>
          <FieldLabel>Connection</FieldLabel>
          <p className="break-all text-sm">
            {connection?.provider_agent_display_name && (
              <>{connection.provider_agent_display_name} · </>
            )}
            {app.settings.resource.connection ?? 'No configured connection'}
          </p>
          <FieldDescription>
            The configured connection is kept when editing this app.
          </FieldDescription>
        </Field>
      ) : (
        <AppConnectionField
          connectionsQuery={lookup.connectionsQuery}
          connections={lookup.connections}
          provider={provider}
          providerLabel={providerLabel}
          selection={lookup.selection}
          savedConnection={savedConnection}
          locked={locked}
          onSelectionChange={lookup.setSelection}
          onNewSlack={connection?.state === 'active' ? undefined : onConnectSlack}
        />
      )}
      {connectionQuery.isError && (
        <p role="alert" className="text-sm">
          Could not load the selected connection.{' '}
          <button type="button" onClick={() => void connectionQuery.refetch()}>
            Retry connection
          </button>
        </p>
      )}
      {lookup.providerMismatch && <p role="alert">Choose a {providerLabel} connection.</p>}
    </>
  )
}

export function AppConnectionField({
  connectionsQuery,
  connections,
  provider,
  providerLabel,
  selection,
  savedConnection,
  locked,
  onSelectionChange,
  onNewSlack,
}: {
  connectionsQuery: ReturnType<typeof useIntegrationConnections>
  connections: IntegrationConnection[]
  provider: ProfileAppProvider
  providerLabel: string
  selection: string
  savedConnection?: IntegrationConnection
  locked: boolean
  onSelectionChange: (selection: string) => void
  onNewSlack?: () => void
}) {
  return (
    <Field>
      <FieldLabel htmlFor="app-connection">Connection</FieldLabel>
      <select
        id="app-connection"
        aria-label="Connection"
        className={selectClass}
        value={savedConnection?.id ?? selection}
        disabled={locked}
        onChange={(e) => {
          onSelectionChange(e.target.value)
        }}
        required
      >
        <option value="">Choose an active connection</option>
        {provider !== 'slack' && <option value="new">New {providerLabel} connection</option>}
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
      {provider === 'slack' && onNewSlack && !locked && (
        <>
          <Button type="button" variant="outline" onClick={onNewSlack}>
            Connect a Slack app through OAuth
          </Button>
          <FieldDescription>
            Connect your workspace first, then choose this app’s capabilities and optional launcher.
          </FieldDescription>
        </>
      )}
      <FieldDescription>
        Connections are shared across project apps. Reuse the same provider account here.
      </FieldDescription>
    </Field>
  )
}

export function NewAppConnectionFields({
  provider,
  providerLabel,
  savedSecret,
  newCredential,
  secretsQuery,
  onDifferentCredentials,
  onNewCredentialChange,
  keyRequired,
  keyPattern,
}: {
  provider: ProfileAppProvider
  providerLabel: string
  savedSecret: string
  newCredential: boolean
  secretsQuery: ReturnType<typeof useProjectAvailableSecrets>
  onDifferentCredentials: () => void
  onNewCredentialChange: (enabled: boolean) => void
  keyRequired: boolean
  keyPattern: string
}) {
  const secrets = useInfiniteQueryItems(secretsQuery).map((access) => access.secret)
  return (
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
              onDifferentCredentials()
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
                onNewCredentialChange(e.target.checked)
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
                  defaultValue={`${providerLabel} credentials`}
                />
              </Field>
              <AppCredentialFields provider={provider} />
            </>
          ) : (
            <Field>
              <FieldLabel htmlFor="existing-secret">Existing project credential</FieldLabel>
              <select
                id="existing-secret"
                aria-label="Existing project credential"
                className={selectClass}
                name="secret"
                required
              >
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
              Interaction public key{keyRequired ? ' (required)' : ' (optional)'}
            </FieldLabel>
            <Input
              id="discord-public-key"
              name="publicKey"
              pattern={keyPattern}
              required={keyRequired}
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
  )
}
