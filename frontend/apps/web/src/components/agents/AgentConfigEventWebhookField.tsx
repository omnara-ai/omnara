import {
  eventWebhookEventTypes,
  eventWebhookUrlError,
} from '@/components/agents/agentConfigEventWebhook'
import { SecretSelect } from '@/components/secrets/SecretTypeaheadField'
import { Field, FieldDescription, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { createResourceMultiCombobox } from '@/components/ui/resource-multi-combobox'

const eventOptions = [{ value: 'all', label: 'All events' }, ...eventWebhookEventTypes]
const EventMultiSelect = createResourceMultiCombobox<(typeof eventOptions)[number]>({
  itemKey: (option) => option.value,
  itemLabel: (option) => option.label,
  placeholder: 'Select events…',
})

export function AgentConfigEventWebhookField({
  orgId,
  projectId,
  url,
  signingSecretId,
  events,
  onEventsChange,
  onUrlChange,
  onSigningSecretIdChange,
}: {
  orgId: string
  projectId: string
  url: string
  signingSecretId: string
  events: string[] | null
  onEventsChange: (events: string[] | null) => void
  onUrlChange: (url: string) => void
  onSigningSecretIdChange: (secretId: string) => void
}) {
  const urlError = eventWebhookUrlError(url)
  const selectedEventTypes = new Set(events)
  const selectedEvents = eventOptions.filter((option) =>
    events === null ? option.value === 'all' : selectedEventTypes.has(option.value),
  )
  return (
    <>
      <Field>
        <FieldLabel htmlFor="agent-config-event-webhook">Event webhook</FieldLabel>
        <Input
          id="agent-config-event-webhook"
          className="mt-2"
          type="url"
          aria-invalid={Boolean(urlError)}
          aria-describedby="agent-config-event-webhook-help"
          placeholder="https://example.com/events"
          value={url}
          onChange={(event) => {
            onUrlChange(event.target.value)
          }}
        />
        {urlError ? (
          <FieldError id="agent-config-event-webhook-help">{urlError}</FieldError>
        ) : (
          <FieldDescription id="agent-config-event-webhook-help">
            Send this agent’s events to an HTTPS URL.
          </FieldDescription>
        )}
      </Field>
      {url.trim() !== '' && (
        <>
          <Field>
            <FieldLabel htmlFor="agent-config-event-webhook-events">Events</FieldLabel>
            <EventMultiSelect
              id="agent-config-event-webhook-events"
              items={eventOptions}
              value={selectedEvents}
              onValueChange={(options) => {
                const values = options.map((option) => option.value)
                onEventsChange(
                  events !== null && values.includes('all')
                    ? null
                    : values.filter((value) => value !== 'all'),
                )
              }}
            />
            {events?.length === 0 && <FieldError>Select at least one event type.</FieldError>}
          </Field>
          <Field>
            <FieldLabel>Signing secret (optional)</FieldLabel>
            <SecretSelect
              orgId={orgId}
              projectId={projectId}
              enabled
              value={signingSecretId}
              onChange={onSigningSecretIdChange}
              placeholder="Select a signing secret…"
            />
            <FieldDescription>
              Use the same secret to verify requests on your server.
            </FieldDescription>
          </Field>
        </>
      )}
    </>
  )
}
