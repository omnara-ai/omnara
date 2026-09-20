import { useProjectApp } from '@omnara/react'
import type { CronTriggerTarget } from '@omnara/sdk'

import { ProjectAppProfilePicker } from '@/components/apps/ProjectAppProfilePicker'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

export function AppCronTriggerFields({
  orgId,
  projectId,
  value,
  onChange,
  editing,
}: {
  orgId: string
  projectId: string
  value: Extract<CronTriggerTarget, { type: 'app_launch' }>
  onChange: (value: Extract<CronTriggerTarget, { type: 'app_launch' }>) => void
  editing: boolean
}) {
  const app = useProjectApp(orgId, projectId, value.app_id)
  const provider = app.data?.provider
  return (
    <>
      <ProjectAppProfilePicker
        orgId={orgId}
        projectId={projectId}
        single
        label="Agent profile"
        description={
          editing
            ? 'The profile cannot be changed. Create a new schedule to use another profile.'
            : 'Each run launches a fresh agent from this profile.'
        }
        disabled={editing}
        value={
          value.agent_profile_id
            ? [{ id: value.agent_profile_id, name: value.agent_profile_id }]
            : []
        }
        onChange={(profiles) => {
          onChange({ ...value, agent_profile_id: profiles[0]?.id ?? '' })
        }}
      />
      <Field>
        <FieldLabel htmlFor="cron-trigger-channel">Channel ID</FieldLabel>
        <Input
          id="cron-trigger-channel"
          required
          pattern={
            provider === 'slack'
              ? '[CG][A-Z0-9]+'
              : provider === 'discord'
                ? '[1-9][0-9]*'
                : undefined
          }
          placeholder={provider === 'slack' ? 'C0123456789' : 'Channel ID'}
          value={value.destination.channel_id}
          onChange={(event) => {
            onChange({
              ...value,
              destination: { ...value.destination, channel_id: event.target.value.trim() },
            })
          }}
        />
        <FieldDescription>
          {provider === 'slack'
            ? 'Use a Slack channel ID beginning with C or G, not a DM or thread. The bot must have access.'
            : provider === 'discord'
              ? 'Use a Discord text or announcement channel ID, not a thread. The bot must have access.'
              : 'Use a channel the bot can access; each run creates a new thread.'}
        </FieldDescription>
      </Field>
      {provider === 'discord' && (
        <Field>
          <FieldLabel htmlFor="cron-trigger-guild">Server ID (optional)</FieldLabel>
          <Input
            id="cron-trigger-guild"
            pattern="[1-9][0-9]*"
            value={value.destination.guild_id ?? ''}
            onChange={(event) => {
              onChange({
                ...value,
                destination: {
                  ...value.destination,
                  guild_id: event.target.value.trim() || undefined,
                },
              })
            }}
          />
        </Field>
      )}
      <Field>
        <FieldLabel htmlFor="cron-trigger-opening">Opening message template</FieldLabel>
        <Textarea
          id="cron-trigger-opening"
          required
          rows={2}
          value={value.opening_message_template}
          onChange={(event) => {
            onChange({ ...value, opening_message_template: event.target.value })
          }}
        />
        <FieldDescription>
          Posted in the channel before the agent starts; its replies go in the new thread.
          {' {{.trigger.name}}'} is the schedule name and {'{{.trigger.local_date}}'} is the date in
          the schedule’s timezone.
        </FieldDescription>
      </Field>
    </>
  )
}
