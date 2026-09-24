import { useCreateCronTrigger, useUpdateCronTrigger } from '@omnara/react'
import {
  type CreateCronTriggerRequest,
  type CronTrigger,
  type CronTriggerDeliveryMode,
  type CronTriggerTarget,
  type IntegrationCronTriggerTarget,
  type UpdateCronTriggerRequest,
} from '@omnara/sdk'
import { useForm } from '@tanstack/react-form'
import cronstrue from 'cronstrue'
import { useState } from 'react'

import {
  cronTriggerDeliveryModeHint,
  cronTriggerDeliveryModeLabel,
  cronTriggerDeliveryModeOptions,
} from '@/components/agents/cron-trigger-delivery-mode'
import { IntegrationCronTriggerFields } from '@/components/integrations/IntegrationCronTriggerFields'
import { useIntegrationScheduleSettings } from '@/components/integrations/useIntegrationScheduleSettings'
import { Button } from '@/components/ui/button'
import {
  Combobox,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxInput,
  ComboboxItem,
  ComboboxList,
} from '@/components/ui/combobox'
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
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { resourceNameValid } from '@/lib/resource-name'
import { errorMessage } from '@/lib/submit-status'

const browserTimezone = new Intl.DateTimeFormat().resolvedOptions().timeZone

interface TimezoneItem {
  zone: string
  label: string
}

function timezoneItemLabel(zone: string) {
  const offset = new Intl.DateTimeFormat('en-US', { timeZone: zone, timeZoneName: 'longOffset' })
    .formatToParts(new Date())
    .find((part) => part.type === 'timeZoneName')?.value
  if (!offset) return zone
  return `${zone} (${offset === 'GMT' ? 'GMT+00:00' : offset})`
}

let timezoneItemsCache: TimezoneItem[] | undefined

function timezoneItems(): TimezoneItem[] {
  if (!timezoneItemsCache) {
    const pinned = new Set(browserTimezone === 'UTC' ? ['UTC'] : [browserTimezone, 'UTC'])
    const rest = Intl.supportedValuesOf('timeZone').filter((zone) => !pinned.has(zone))
    timezoneItemsCache = [...pinned, ...rest].map((zone) => ({
      zone,
      label: timezoneItemLabel(zone),
    }))
  }
  return timezoneItemsCache
}

function cronDescription(expression: string) {
  if (expression.trim().split(/\s+/).length !== 5) return undefined
  try {
    return cronstrue.toString(expression)
  } catch {
    return undefined
  }
}

function cronTriggerFormValid(
  value: CronTriggerFormValues,
  integrationSettingsValid: (settings: IntegrationCronTriggerTarget['settings']) => boolean,
) {
  return (
    resourceNameValid(value.name) &&
    value.cron.trim() !== '' &&
    value.timezone.trim() !== '' &&
    (value.integrationTarget
      ? integrationSettingsValid(value.integrationTarget.settings)
      : value.messageTemplate.trim() !== '')
  )
}

interface CronTriggerFormValues {
  name: string
  cron: string
  timezone: string
  messageTemplate: string
  deliveryMode: CronTriggerDeliveryMode
  integrationTarget?: Extract<CronTriggerTarget, { type: 'integration' }>
}

function CronTriggerFormDialog({
  open,
  onOpenChange,
  title,
  description,
  submitLabel,
  errorFallback,
  target,
  defaultValues,
  isPending,
  onSubmit,
  orgId,
  projectId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: string
  submitLabel: string
  errorFallback: string
  target: CronTriggerTarget
  defaultValues: CronTriggerFormValues
  isPending: boolean
  onSubmit: (value: CronTriggerFormValues) => Promise<void>
  orgId: string
  projectId: string
}) {
  const [error, setError] = useState('')
  const integrationSchedule = useIntegrationScheduleSettings(
    orgId,
    projectId,
    target.type === 'integration' ? target.integration_id : '',
  )
  const form = useForm({
    defaultValues,
    onSubmit: async ({ value }) => {
      if (!cronTriggerFormValid(value, integrationSchedule.valid)) return
      setError('')
      try {
        await onSubmit(value)
        onOpenChange(false)
      } catch (err) {
        setError(errorMessage(err, errorFallback))
      }
    },
  })
  function submitForm(element: HTMLFormElement) {
    if (element.checkValidity()) void form.handleSubmit()
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next && isPending) return
        onOpenChange(next)
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            event.preventDefault()
            submitForm(event.currentTarget)
          }}
        >
          <FieldGroup>
            <form.Field name="name">
              {(field) => (
                <Field>
                  <FieldLabel htmlFor="cron-trigger-name">Name</FieldLabel>
                  <Input
                    id="cron-trigger-name"
                    required
                    value={field.state.value}
                    onChange={(event) => {
                      field.handleChange(event.target.value)
                    }}
                  />
                  <ResourceNameFieldError value={field.state.value} />
                </Field>
              )}
            </form.Field>
            {target.type === 'integration' && (
              <form.Field name="integrationTarget">
                {(field) =>
                  field.state.value && (
                    <IntegrationCronTriggerFields
                      orgId={orgId}
                      projectId={projectId}
                      schedule={integrationSchedule}
                      value={field.state.value}
                      onChange={field.handleChange}
                    />
                  )
                }
              </form.Field>
            )}
            <form.Field name="cron">
              {(field) => (
                <Field>
                  <FieldLabel htmlFor="cron-trigger-cron">Cron expression</FieldLabel>
                  <Input
                    id="cron-trigger-cron"
                    required
                    placeholder="0 9 * * 1-5"
                    className="font-mono"
                    value={field.state.value}
                    onChange={(event) => {
                      field.handleChange(event.target.value)
                    }}
                  />
                  <FieldDescription>
                    {cronDescription(field.state.value) ??
                      'Five fields: minute, hour, day of month, month, day of week.'}
                  </FieldDescription>
                </Field>
              )}
            </form.Field>
            <form.Field name="timezone">
              {(field) => (
                <Field>
                  <FieldLabel htmlFor="cron-trigger-timezone">Timezone</FieldLabel>
                  <Combobox
                    items={timezoneItems()}
                    value={timezoneItems().find((item) => item.zone === field.state.value) ?? null}
                    onValueChange={(item: TimezoneItem | null) => {
                      field.handleChange(item?.zone ?? '')
                    }}
                    itemToStringLabel={(item: TimezoneItem) => item.label}
                    itemToStringValue={(item: TimezoneItem) => item.zone}
                    isItemEqualToValue={(item: TimezoneItem, other: TimezoneItem) =>
                      item.zone === other.zone
                    }
                  >
                    <ComboboxInput id="cron-trigger-timezone" required />
                    <ComboboxContent>
                      <ComboboxEmpty>No timezones match.</ComboboxEmpty>
                      <ComboboxList>
                        {(item: TimezoneItem) => (
                          <ComboboxItem key={item.zone} value={item}>
                            {item.label}
                          </ComboboxItem>
                        )}
                      </ComboboxList>
                    </ComboboxContent>
                  </Combobox>
                  <FieldDescription>The schedule is evaluated in this timezone.</FieldDescription>
                </Field>
              )}
            </form.Field>
            {target.type !== 'integration' && (
              <form.Field name="messageTemplate">
                {(field) => (
                  <Field>
                    <FieldLabel htmlFor="cron-trigger-message">Message</FieldLabel>
                    <Textarea
                      id="cron-trigger-message"
                      required
                      rows={4}
                      value={field.state.value}
                      onChange={(event) => {
                        field.handleChange(event.target.value)
                      }}
                    />
                    <FieldDescription>
                      Sent on each firing. Go template syntax with {'{{.trigger.name}}'},{' '}
                      {'{{.trigger.fired_at}}'}, {'{{.trigger.last_fired_at}}'}, and{' '}
                      {'{{.trigger.local_date}}'} available. The local date uses the schedule’s
                      timezone.
                    </FieldDescription>
                  </Field>
                )}
              </form.Field>
            )}
            {target.type === 'agent' && (
              <form.Field name="deliveryMode">
                {(field) => (
                  <Field>
                    <FieldLabel htmlFor="cron-trigger-delivery-mode">Delivery</FieldLabel>
                    <Select
                      value={field.state.value}
                      onValueChange={(value: CronTriggerDeliveryMode) => {
                        field.handleChange(value)
                      }}
                    >
                      <SelectTrigger id="cron-trigger-delivery-mode" className="w-full">
                        <SelectValue>{cronTriggerDeliveryModeLabel(field.state.value)}</SelectValue>
                      </SelectTrigger>
                      <SelectContent>
                        {cronTriggerDeliveryModeOptions.map((option) => (
                          <SelectItem key={option.value} value={option.value}>
                            {option.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <FieldDescription>
                      {cronTriggerDeliveryModeHint(field.state.value)}
                    </FieldDescription>
                  </Field>
                )}
              </form.Field>
            )}
            {error && <p className="text-destructive whitespace-pre-wrap text-sm">{error}</p>}
            <DialogFooter>
              <form.Subscribe
                selector={(state) =>
                  [
                    cronTriggerFormValid(state.values, integrationSchedule.valid),
                    state.isSubmitting,
                  ] as const
                }
              >
                {([valid, isSubmitting]) => (
                  <Button type="submit" disabled={isSubmitting || !valid} loading={isSubmitting}>
                    {submitLabel}
                  </Button>
                )}
              </form.Subscribe>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function targetDescription(target: CronTriggerTarget, targetLabel?: string) {
  if (target.type === 'integration') {
    return `Each run asks ${targetLabel ?? 'this integration'} to perform its scheduled action using these settings. Changes apply to future runs; work already prepared keeps its saved settings.`
  }
  if (target.type === 'profile') {
    return `Each firing launches a new agent from ${targetLabel ?? 'this profile'} and sends the message as its initial prompt. To message an agent that already exists, add a schedule from that agent's page instead.`
  }
  return `Each firing sends the message to ${targetLabel ?? 'this existing agent'}. To launch a new agent on a schedule, add one from the agent profile instead.`
}

export function CreateCronTriggerDialog({
  open,
  onOpenChange,
  orgId,
  projectId,
  target,
  targetLabel,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  projectId: string
  target: CronTriggerTarget
  targetLabel: string
  onCreated?: (trigger: CronTrigger) => void
}) {
  const createTrigger = useCreateCronTrigger(orgId, projectId)
  return (
    <CronTriggerFormDialog
      orgId={orgId}
      projectId={projectId}
      open={open}
      onOpenChange={onOpenChange}
      title="Add cron schedule"
      description={targetDescription(target, targetLabel)}
      submitLabel="Create schedule"
      errorFallback="Could not create schedule"
      target={target}
      defaultValues={{
        name: '',
        cron: '',
        timezone: browserTimezone,
        messageTemplate: '',
        deliveryMode: 'queued',
        integrationTarget: target.type === 'integration' ? target : undefined,
      }}
      isPending={createTrigger.isPending}
      onSubmit={async (value) => {
        const request: CreateCronTriggerRequest = {
          name: value.name,
          target:
            target.type === 'agent'
              ? { ...target, delivery_mode: value.deliveryMode }
              : (value.integrationTarget ?? target),
          cron: value.cron.trim(),
          timezone: value.timezone.trim(),
        }
        if (target.type !== 'integration') request.message_template = value.messageTemplate
        const trigger = await createTrigger.mutateAsync(request)
        onCreated?.(trigger)
      }}
    />
  )
}

export function EditCronTriggerDialog({
  open,
  onOpenChange,
  orgId,
  projectId,
  trigger,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  projectId: string
  trigger: CronTrigger
}) {
  const updateTrigger = useUpdateCronTrigger(orgId, projectId)
  return (
    <CronTriggerFormDialog
      orgId={orgId}
      projectId={projectId}
      open={open}
      onOpenChange={onOpenChange}
      title="Edit cron schedule"
      description={targetDescription(trigger.target)}
      submitLabel="Save changes"
      errorFallback="Could not update schedule"
      target={trigger.target}
      defaultValues={{
        name: trigger.name,
        cron: trigger.cron,
        timezone: trigger.timezone,
        messageTemplate: trigger.message_template ?? '',
        deliveryMode:
          trigger.target.type === 'agent' ? (trigger.target.delivery_mode ?? 'queued') : 'queued',
        integrationTarget: trigger.target.type === 'integration' ? trigger.target : undefined,
      }}
      isPending={updateTrigger.isPending}
      onSubmit={async (value) => {
        const update: UpdateCronTriggerRequest & { cronTriggerID: string } = {
          cronTriggerID: trigger.id,
          name: value.name,
          cron: value.cron.trim(),
          timezone: value.timezone.trim(),
        }
        if (trigger.target.type !== 'integration') update.message_template = value.messageTemplate
        if (trigger.target.type === 'agent') {
          update.target = { ...trigger.target, delivery_mode: value.deliveryMode }
        }
        if (trigger.target.type === 'integration' && value.integrationTarget) {
          update.target = value.integrationTarget
        }
        await updateTrigger.mutateAsync(update)
      }}
    />
  )
}
