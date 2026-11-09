import { useCreateEventWebhook, useUpdateEventWebhook } from '@omnara/react'
import type { EventWebhook, EventWebhookEventType, EventWebhookScope, Secret } from '@omnara/sdk'
import { useForm } from '@tanstack/react-form'
import { useId, useState } from 'react'

import { EventWebhookScopeField } from '@/components/projects/EventWebhookScopeField'
import { NewSecretDialog } from '@/components/secrets/NewSecretDialog'
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
import { resourceNameSuggestion } from '@/lib/resource-name'
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

function signingKeyError(value: string) {
  const key = value.replace(/^whsec_/, '')
  const padding = key.endsWith('==') ? 2 : key.endsWith('=') ? 1 : 0
  const bytes = (key.length / 4) * 3 - padding
  if (
    /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(key) &&
    bytes >= 24 &&
    bytes <= 64
  )
    return undefined
  return 'Use a base64 key of 24–64 bytes, like one from openssl rand -base64 32.'
}

function generateSigningKey() {
  const bytes = crypto.getRandomValues(new Uint8Array(32))
  return `whsec_${btoa(String.fromCharCode(...bytes))}`
}

function signingSecretName(url: string) {
  const fallback = 'webhook-signing-secret'
  if (urlError(url)) return fallback
  return resourceNameSuggestion(`${new URL(url.trim()).hostname}-signing-secret`, fallback)
}

export function EventWebhookDialog({
  orgId,
  projectId,
  webhook,
  onClose,
}: {
  orgId: string
  projectId: string
  webhook?: EventWebhook
  onClose: () => void
}) {
  const create = useCreateEventWebhook(orgId, projectId)
  const update = useUpdateEventWebhook(orgId, projectId)
  const pending = create.isPending || update.isPending
  const [error, setError] = useState('')
  const [creatingSecret, setCreatingSecret] = useState(false)
  const [createdSecret, setCreatedSecret] = useState<Secret>()
  const id = useId()
  const eventTypes: EventWebhookEventType[] = webhook?.event_types ?? ['tool_call_update']
  const initialScope: EventWebhookScope = webhook?.scope ?? { type: 'project' }
  const initialSelectionId =
    initialScope.type === 'project'
      ? undefined
      : initialScope.type === 'agent'
        ? initialScope.agent_id
        : initialScope.agent_profile_id
  const form = useForm({
    defaultValues: {
      scope: initialScope,
      url: webhook?.url ?? '',
      eventTypes,
      signingSecretId: webhook?.signing_secret_id ?? '',
    },
    onSubmit: async ({ value }) => {
      if (
        pending ||
        urlError(value.url) ||
        value.eventTypes.length === 0 ||
        !hasWebhookSelection(value.scope)
      )
        return
      setError('')
      try {
        const settings = {
          scope: value.scope,
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
            Choose the events to send to your endpoint. Notifications already queued can still
            arrive after an edit.
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
              <form.Field name="scope">
                {(field) => (
                  <EventWebhookScopeField
                    orgId={orgId}
                    projectId={projectId}
                    value={field.state.value}
                    initialSelection={
                      initialSelectionId
                        ? {
                            id: initialSelectionId,
                            name: webhook?.scope_name ?? initialSelectionId,
                          }
                        : undefined
                    }
                    onChange={field.handleChange}
                  />
                )}
              </form.Field>
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
                      key={String(creatingSecret)}
                      inputId={id + '-secret'}
                      orgId={orgId}
                      projectId={projectId}
                      enabled
                      value={field.state.value}
                      onChange={field.handleChange}
                      placeholder="Select a signing secret…"
                      knownSecret={createdSecret}
                      onCreateSecret={() => {
                        setCreatingSecret(true)
                      }}
                    />
                    {creatingSecret && (
                      <NewSecretDialog
                        orgId={orgId}
                        projectId={projectId}
                        defaultName={signingSecretName(form.getFieldValue('url'))}
                        valueError={signingKeyError}
                        generateValue={generateSigningKey}
                        onClose={() => {
                          setCreatingSecret(false)
                        }}
                        onCreated={(secret) => {
                          setCreatedSecret(secret)
                          field.handleChange(secret.id)
                          setCreatingSecret(false)
                        }}
                      />
                    )}
                    <FieldDescription>
                      A base64 key, like one from <code>openssl rand -base64 32</code>. Verify
                      requests on your server with the same key.
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
                    disabled={
                      pending ||
                      !!urlError(value.url) ||
                      value.eventTypes.length === 0 ||
                      !hasWebhookSelection(value.scope)
                    }
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

function hasWebhookSelection(scope: EventWebhookScope) {
  return (
    scope.type === 'project' ||
    (scope.type === 'profile' ? !!scope.agent_profile_id : !!scope.agent_id)
  )
}
