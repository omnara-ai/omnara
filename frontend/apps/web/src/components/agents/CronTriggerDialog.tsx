import { useCreateCronTrigger, useUpdateCronTrigger } from '@omnara/react'
import {
  type CronTrigger,
  type CronTriggerDeliveryMode,
  type CronTriggerTarget,
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

function cronTriggerFormValid(value: {
  name: string
  cron: string
  timezone: string
  messageTemplate: string
}) {
  return (
    resourceNameValid(value.name) &&
    value.cron.trim() !== '' &&
    value.timezone.trim() !== '' &&
    value.messageTemplate.trim() !== ''
  )
}

interface CronTriggerFormValues {
  name: string
  cron: string
  timezone: string
  messageTemplate: string
  deliveryMode: CronTriggerDeliveryMode
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
}) {
  const [error, setError] = useState('')
  const form = useForm({
    defaultValues,
    onSubmit: async ({ value }) => {
      if (!cronTriggerFormValid(value)) return
      setError('')
      try {
        await onSubmit(value)
        onOpenChange(false)
      } catch (err) {
        setError(errorMessage(err, errorFallback))
      }
    },
  })

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
            void form.handleSubmit()
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
                    {'{{.trigger.fired_at}}'}, and {'{{.trigger.last_fired_at}}'} available.
                  </FieldDescription>
                </Field>
              )}
            </form.Field>
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
                  [cronTriggerFormValid(state.values), state.isSubmitting] as const
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
      }}
      isPending={createTrigger.isPending}
      onSubmit={async (value) => {
        const trigger = await createTrigger.mutateAsync({
          name: value.name,
          target:
            target.type === 'agent' ? { ...target, delivery_mode: value.deliveryMode } : target,
          cron: value.cron.trim(),
          timezone: value.timezone.trim(),
          message_template: value.messageTemplate,
        })
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
        messageTemplate: trigger.message_template,
        deliveryMode:
          trigger.target.type === 'agent' ? (trigger.target.delivery_mode ?? 'queued') : 'queued',
      }}
      isPending={updateTrigger.isPending}
      onSubmit={async (value) => {
        const update: UpdateCronTriggerRequest & { cronTriggerID: string } = {
          cronTriggerID: trigger.id,
          name: value.name,
          cron: value.cron.trim(),
          timezone: value.timezone.trim(),
          message_template: value.messageTemplate,
        }
        if (trigger.target.type === 'agent') {
          update.target = { ...trigger.target, delivery_mode: value.deliveryMode }
        }
        await updateTrigger.mutateAsync(update)
      }}
    />
  )
}
