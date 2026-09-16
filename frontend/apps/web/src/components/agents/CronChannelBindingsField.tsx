import { useIntegrationInstalls, useRegisteredChannels } from '@omnara/react'
import type {
  AttachAgentChannelRequest,
  ChannelGrants,
  IntegrationInstall,
  RegisteredChannel,
} from '@omnara/sdk'
import { useCallback, useEffect, useState } from 'react'

import { channelBindingError } from '@/components/agents/cron-channel-bindings'
import { Button } from '@/components/ui/button'
import { CheckboxField, Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'

const ConnectionCombobox = createResourceCombobox<IntegrationInstall>({
  itemKey: (install) => install.id,
  itemLabel: (install) => install.display_name || (install.provider_account_ref ?? install.id),
  placeholder: 'Search connections…',
  emptyMessage: 'No active connections found.',
})
const ChannelCombobox = createResourceCombobox<RegisteredChannel>({
  itemKey: (channel) => channel.channel_id,
  itemLabel: channelLabel,
  placeholder: 'Filter loaded channels…',
  emptyMessage: 'No more channels on this page. Load more or add one in Integrations.',
})

function channelLabel(channel: RegisteredChannel) {
  return channel.name ? `${channel.name} (${channel.provider_ref})` : channel.provider_ref
}

function ChannelChoice({
  orgId,
  projectId,
  installId,
  bindings,
  disabled,
  onAdd,
  onChannelsLoaded,
}: {
  orgId: string
  projectId: string
  installId: string
  bindings: AttachAgentChannelRequest[]
  disabled: boolean
  onAdd: (channel: RegisteredChannel) => void
  onChannelsLoaded: (channels: RegisteredChannel[]) => void
}) {
  const query = useRegisteredChannels(orgId, projectId, installId)
  const channels = useInfiniteQueryItems(query)
  useEffect(() => {
    if (query.data) onChannelsLoaded(query.data.pages.flatMap((page) => page.data))
  }, [query.data, onChannelsLoaded])
  const [selected, setSelected] = useState<RegisteredChannel | null>(null)
  return (
    <Field>
      <FieldLabel htmlFor="cron-channel">Channel</FieldLabel>
      <ChannelCombobox
        id="cron-channel"
        items={channels.filter(
          (channel) => !bindings.some((binding) => binding.channel_id === channel.channel_id),
        )}
        query={query}
        value={selected}
        onValueChange={setSelected}
        placeholder="Choose channel…"
        disabled={disabled}
      />
      <Button
        type="button"
        variant="outline"
        className="justify-self-start"
        disabled={
          disabled ||
          !selected ||
          bindings.some((binding) => binding.channel_id === selected.channel_id)
        }
        onClick={() => {
          if (!selected || disabled) return
          onAdd(selected)
          setSelected(null)
        }}
      >
        Add channel access
      </Button>
    </Field>
  )
}

function GrantFields({
  value,
  onChange,
  disabled,
}: {
  value: ChannelGrants
  onChange: (grants: ChannelGrants) => void
  disabled: boolean
}) {
  return (
    <div className="flex flex-wrap gap-4">
      {(['read', 'send', 'receive'] as const).map((grant) => (
        <CheckboxField
          key={grant}
          className="w-auto"
          label={{ read: 'Read', send: 'Send', receive: 'Receive' }[grant]}
          checked={value[grant]}
          disabled={disabled}
          onChange={(event) => {
            onChange({ ...value, [grant]: event.target.checked })
          }}
        />
      ))}
    </div>
  )
}

export function CronChannelBindingsField({
  orgId,
  projectId,
  value,
  onChange,
  disabled,
}: {
  orgId: string
  projectId: string
  value: AttachAgentChannelRequest[]
  onChange: (bindings: AttachAgentChannelRequest[]) => void
  disabled: boolean
}) {
  const search = useTypeaheadSearch()
  const connectionsQuery = useIntegrationInstalls(orgId, projectId, {
    filters: search.filters,
    sort: 'name',
  })
  const connections = useInfiniteQueryItems(connectionsQuery)
  const [connection, setConnection] = useState<IntegrationInstall | null>(null)
  const [names, setNames] = useState<Record<string, string>>({})
  const [replyDrafts, setReplyDrafts] = useState<Record<string, ChannelGrants>>({})
  const connectionName = connection
    ? connection.display_name || (connection.provider_account_ref ?? connection.id)
    : ''
  const rememberChannels = useCallback(
    (channels: RegisteredChannel[]) => {
      setNames((current) => {
        const next = { ...current }
        for (const channel of channels)
          next[channel.channel_id] = `${connectionName} · ${channelLabel(channel)}`
        return channels.some((channel) => next[channel.channel_id] !== current[channel.channel_id])
          ? next
          : current
      })
    },
    [connectionName],
  )
  function update(binding: AttachAgentChannelRequest) {
    if (!disabled)
      onChange(value.map((item) => (item.channel_id === binding.channel_id ? binding : item)))
  }
  return (
    <section className="grid gap-4" aria-label="Channels">
      <div>
        <p className="text-sm font-medium">Channels (optional)</p>
        <FieldDescription>
          Give each new scheduled agent access to selected channels. Existing agents keep their
          permissions; a launch already in progress may use the previous settings. Include what to
          post in the schedule message.
        </FieldDescription>
      </div>
      {value.map((binding) => {
        const label = names[binding.channel_id] ?? binding.channel_id
        const error = channelBindingError(binding)
        return (
          <fieldset
            key={binding.channel_id}
            className="grid gap-3 rounded-md border p-3"
            disabled={disabled}
          >
            <legend className="max-w-full break-all px-1 text-sm font-medium">{label}</legend>
            <GrantFields
              value={binding.grants}
              disabled={disabled}
              onChange={(grants) => {
                update({ ...binding, grants })
              }}
            />
            <p className="text-muted-foreground text-xs">
              Receive allows incoming messages when the connection routes them to this agent.
            </p>
            <CheckboxField
              label="Allow reply threads"
              description="Permissions for a new reply thread created when this agent sends a message. They do not apply to further child threads."
              checked={binding.reply_channel_grants !== undefined}
              disabled={disabled}
              onChange={(event) => {
                const next = { ...binding }
                if (event.target.checked)
                  next.reply_channel_grants = replyDrafts[binding.channel_id] ?? {
                    receive: true,
                    read: true,
                    send: true,
                  }
                else {
                  if (binding.reply_channel_grants) {
                    const grants = binding.reply_channel_grants
                    setReplyDrafts((current) => ({ ...current, [binding.channel_id]: grants }))
                  }
                  delete next.reply_channel_grants
                }
                update(next)
              }}
            />
            {binding.reply_channel_grants && (
              <fieldset className="grid gap-2 pl-3">
                <legend className="mb-2 text-sm">Reply thread permissions</legend>
                <GrantFields
                  value={binding.reply_channel_grants}
                  disabled={disabled}
                  onChange={(grants) => {
                    update({ ...binding, reply_channel_grants: grants })
                  }}
                />
              </fieldset>
            )}
            {error && (
              <p role="alert" className="text-destructive text-sm">
                {error}
              </p>
            )}
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="justify-self-end"
              disabled={disabled}
              aria-label={`Remove ${label}`}
              onClick={() => {
                onChange(value.filter((item) => item.channel_id !== binding.channel_id))
              }}
            >
              Remove
            </Button>
          </fieldset>
        )
      })}
      <Field>
        <FieldLabel htmlFor="cron-connection">Connection</FieldLabel>
        <ConnectionCombobox
          id="cron-connection"
          items={connections.filter((item) => item.state === 'active')}
          search={search}
          query={connectionsQuery}
          value={connection}
          onValueChange={setConnection}
          disabled={disabled || value.length >= 64}
        />
      </Field>
      {value.length >= 64 && (
        <p className="text-muted-foreground text-sm">
          Maximum 64 channels. Remove one to add another.
        </p>
      )}
      {connection && (
        <ChannelChoice
          key={connection.id}
          orgId={orgId}
          projectId={projectId}
          installId={connection.id}
          bindings={value}
          disabled={disabled || value.length >= 64}
          onChannelsLoaded={rememberChannels}
          onAdd={(channel) => {
            if (
              disabled ||
              value.length >= 64 ||
              value.some((binding) => binding.channel_id === channel.channel_id)
            )
              return
            onChange([
              ...value,
              {
                channel_id: channel.channel_id,
                grants: { read: true, send: true, receive: false },
                reply_channel_grants: { read: true, send: true, receive: true },
              },
            ])
          }}
        />
      )}
      <p className="text-muted-foreground text-sm">
        Missing a channel?{' '}
        <a
          className="text-foreground underline underline-offset-2"
          href={`/projects/${projectId}/integrations`}
          target="_blank"
          rel="noopener noreferrer"
        >
          Set up channels in Integrations
        </a>
        .
      </p>
    </section>
  )
}
