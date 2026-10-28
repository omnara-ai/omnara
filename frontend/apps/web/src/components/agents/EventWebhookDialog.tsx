import { useCreateEventWebhook, useUpdateEventWebhook } from '@omnara/react'
import type { EventWebhook, EventWebhookEventType, EventWebhookScope } from '@omnara/sdk'
import { useForm } from '@tanstack/react-form'
import { useId, useState } from 'react'

import { SecretSelect } from '@/components/secrets/SecretTypeaheadField'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { createResourceMultiCombobox } from '@/components/ui/resource-multi-combobox'
import { errorMessage } from '@/lib/submit-status'

const eventTypeOptions: { value: EventWebhookEventType; label: string }[] = [
  { value: 'agent_input', label: 'Agent input' },
  { value: 'model_output', label: 'Model output' },
  { value: 'tool_result', label: 'Tool result' },
  { value: 'context_checkpoint', label: 'Context checkpoint' },
  { value: 'tool_call_update', label: 'Tool-call updates' },
]
const eventTypeSelectOptions = [{ value: 'all', label: 'All event types' }, ...eventTypeOptions]
const EventTypeMultiSelect = createResourceMultiCombobox<(typeof eventTypeSelectOptions)[number]>({
  itemKey: (option) => option.value,
  itemLabel: (option) => option.label,
  placeholder: 'Select event types…',
})

function urlError(value: string) {
  if (!value.trim()) return 'Enter an HTTPS URL.'
  try {
    if (new URL(value.trim()).protocol !== 'https:') return 'Use an HTTPS URL.'
  } catch {
    return 'Enter a valid HTTPS URL.'
  }
  return undefined
}

export function EventWebhookDialog({
  orgId,
  projectId,
  scope,
  webhook,
  onClose,
}: {
  orgId: string
  projectId: string
  scope: EventWebhookScope
  webhook?: EventWebhook
  onClose: () => void
}) {
  const create = useCreateEventWebhook(orgId, projectId)
  const update = useUpdateEventWebhook(orgId, projectId)
  const pending = create.isPending || update.isPending
  const [error, setError] = useState('')
  const id = useId()
  const eventTypes: EventWebhookEventType[] = webhook?.event_types ?? ['tool_call_update']
  const form = useForm({
    defaultValues: {
      url: webhook?.url ?? '',
      eventTypes,
      signingSecretId: webhook?.signing_secret_id ?? '',
    },
    onSubmit: async ({ value }) => {
      if (pending || urlError(value.url) || value.eventTypes.length === 0) return
      setError('')
      try {
        const settings = {
          url: value.url.trim(),
          event_types: value.eventTypes,
        }
        if (webhook)
          await update.mutateAsync({
            ...settings,
            webhookID: webhook.id,
            signing_secret_id: value.signingSecretId || null,
          })
        else
          await create.mutateAsync({
            ...settings,
            scope,
            signing_secret_id: value.signingSecretId || undefined,
          })
        onClose()
      } catch (err) {
        setError(errorMessage(err, 'Could not save webhook'))
      }
    },
  })

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !pending) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{webhook ? 'Edit webhook' : 'Add webhook'}</DialogTitle>
          <DialogDescription>
            {scope.type === 'profile'
              ? 'Receive future events from existing and new agents of this profile.'
              : 'Receive future events from this agent only, not its descendants.'}
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void form.handleSubmit()
          }}
        >
          <fieldset disabled={pending}>
            <FieldGroup>
              <form.Field name="url">
                {(field) => (
                  <Field>
                    <FieldLabel htmlFor={id + '-url'}>Webhook URL</FieldLabel>
                    <Input
                      id={id + '-url'}
                      type="url"
                      required
                      maxLength={2048}
                      placeholder="https://example.com/events"
                      value={field.state.value}
                      onChange={(event) => {
                        field.handleChange(event.target.value)
                      }}
                    />
                    {field.state.value && urlError(field.state.value) && (
                      <FieldError>{urlError(field.state.value)}</FieldError>
                    )}
                  </Field>
                )}
              </form.Field>
              <form.Field name="eventTypes">
                {(field) => (
                  <Field>
                    <FieldLabel htmlFor={id + '-event-types'}>Event types</FieldLabel>
                    <EventTypeMultiSelect
                      id={id + '-event-types'}
                      closeOnSelect
                      items={eventTypeSelectOptions}
                      value={eventTypeOptions.filter((option) =>
                        field.state.value.includes(option.value),
                      )}
                      onValueChange={(options) => {
                        const selected = new Set(options.map((option) => option.value))
                        field.handleChange(
                          eventTypeOptions
                            .filter((option) => selected.has('all') || selected.has(option.value))
                            .map((option) => option.value),
                        )
                      }}
                    />
                    {field.state.value.length === 0 && (
                      <FieldError>Select at least one event type.</FieldError>
                    )}
                  </Field>
                )}
              </form.Field>
              <form.Field name="signingSecretId">
                {(field) => (
                  <Field>
                    <FieldLabel htmlFor={id + '-secret'}>Signing secret (optional)</FieldLabel>
                    <SecretSelect
                      inputId={id + '-secret'}
                      orgId={orgId}
                      projectId={projectId}
                      enabled
                      value={field.state.value}
                      onChange={field.handleChange}
                      placeholder="Select a signing secret…"
                    />
                    <FieldDescription>
                      Use the same secret to verify requests on your server.
                    </FieldDescription>
                  </Field>
                )}
              </form.Field>
              {error && (
                <p role="alert" className="text-destructive text-sm">
                  {error}
                </p>
              )}
            </FieldGroup>
            <DialogFooter className="mt-6">
              <Button type="button" variant="outline" onClick={onClose}>
                Cancel
              </Button>
              <form.Subscribe selector={(state) => state.values}>
                {(value) => (
                  <Button
                    type="submit"
                    loading={pending}
                    disabled={pending || !!urlError(value.url) || value.eventTypes.length === 0}
                  >
                    {webhook ? 'Save changes' : 'Create webhook'}
                  </Button>
                )}
              </form.Subscribe>
            </DialogFooter>
          </fieldset>
        </form>
      </DialogContent>
    </Dialog>
  )
}
