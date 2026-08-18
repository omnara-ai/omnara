import {
  type CronTriggerListFilters,
  useCreateCronTrigger,
  useCronTriggers,
  useDeleteCronTrigger,
  useUpdateCronTrigger,
} from '@omnara/react'
import { type CronTrigger, type CronTriggerTarget } from '@omnara/sdk'
import { useForm } from '@tanstack/react-form'
import cronstrue from 'cronstrue'
import { Trash2 } from 'lucide-react'
import { useState } from 'react'

import { Badge } from '@/components/ui/badge'
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
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { formatDateTime } from '@/lib/format'
import { errorMessage } from '@/lib/submit-status'

const nextFireFormatter = new Intl.DateTimeFormat(undefined, {
  year: 'numeric',
  month: 'short',
  day: 'numeric',
  hour: 'numeric',
  minute: '2-digit',
  timeZoneName: 'short',
})

function nextFireLabel(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  return ` · next ${nextFireFormatter.format(date)}`
}

export function CronTriggersList({
  orgId,
  projectId,
  canManage,
  filters,
  emptyMessage,
}: {
  orgId: string
  projectId: string
  canManage: boolean
  filters: CronTriggerListFilters
  emptyMessage: string
}) {
  const query = useCronTriggers(orgId, projectId, { filters })
  const triggers = useInfiniteQueryItems(query)
  const updateTrigger = useUpdateCronTrigger(orgId, projectId)
  const deleteTrigger = useDeleteCronTrigger(orgId, projectId)

  return (
    <div className="flex flex-col gap-2">
      {triggers.length > 0 ? (
        <ul className="bg-background flex flex-col divide-y rounded-md border">
          {triggers.map((trigger) => (
            <li key={trigger.id} className="flex items-center justify-between gap-3 px-3 py-2">
              <div className="flex min-w-0 flex-col gap-0.5">
                <div className="flex min-w-0 items-center gap-2 text-sm">
                  <span className="truncate font-medium">{trigger.name}</span>
                  {!trigger.enabled && <Badge variant="secondary">Disabled</Badge>}
                  {trigger.failure_report && <Badge variant="destructive">Failing</Badge>}
                </div>
                <p className="text-muted-foreground truncate text-xs">
                  <span className="font-mono">{trigger.cron}</span> · {trigger.timezone}
                  {trigger.next_fire_at && nextFireLabel(trigger.next_fire_at)}
                </p>
                {trigger.failure_report && (
                  <p className="text-destructive break-words text-xs">
                    Failed {formatDateTime(trigger.failure_report.failed_at)}:{' '}
                    {trigger.failure_report.message} ·{' '}
                    {trigger.failure_report.will_retry ? 'will retry' : 'will not retry'}
                  </p>
                )}
              </div>
              {canManage && (
                <div className="flex shrink-0 items-center gap-1">
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={
                      updateTrigger.isPending &&
                      updateTrigger.variables.cronTriggerID === trigger.id
                    }
                    onClick={() => {
                      updateTrigger.mutate(
                        { cronTriggerID: trigger.id, enabled: !trigger.enabled },
                        {
                          onError: (error) => {
                            window.alert(errorMessage(error, 'Could not update schedule'))
                          },
                        },
                      )
                    }}
                  >
                    {trigger.enabled ? 'Disable' : 'Enable'}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={deleteTrigger.isPending && deleteTrigger.variables === trigger.id}
                    onClick={() => {
                      if (!window.confirm(`Delete schedule ${trigger.name}?`)) return
                      deleteTrigger.mutate(trigger.id, {
                        onError: (error) => {
                          window.alert(errorMessage(error, 'Could not delete schedule'))
                        },
                      })
                    }}
                  >
                    <Trash2 />
                  </Button>
                </div>
              )}
            </li>
          ))}
        </ul>
      ) : query.isPending ? (
        <Spinner className="text-muted-foreground size-4" />
      ) : query.isError ? (
        <div className="flex items-center gap-3">
          <p className="text-muted-foreground text-sm">Couldn&rsquo;t load schedules.</p>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              void query.refetch()
            }}
          >
            Retry
          </Button>
        </div>
      ) : (
        <div className="border-border bg-background/60 text-muted-foreground flex min-h-16 items-center justify-center rounded-md border border-dashed px-4 text-sm">
          {emptyMessage}
        </div>
      )}
      {query.hasNextPage && (
        <Button
          size="sm"
          variant="outline"
          className="self-start"
          disabled={query.isFetchingNextPage}
          onClick={() => {
            void query.fetchNextPage()
          }}
        >
          Show more
        </Button>
      )}
    </div>
  )
}

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

function createCronTriggerFormValid(value: {
  name: string
  cron: string
  timezone: string
  messageTemplate: string
}) {
  return (
    value.name.trim() !== '' &&
    value.cron.trim() !== '' &&
    value.timezone.trim() !== '' &&
    value.messageTemplate.trim() !== ''
  )
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
  const [error, setError] = useState('')
  const form = useForm({
    defaultValues: {
      name: '',
      cron: '',
      timezone: browserTimezone,
      messageTemplate: '',
    },
    onSubmit: async ({ value }) => {
      if (!createCronTriggerFormValid(value)) return
      setError('')
      try {
        const trigger = await createTrigger.mutateAsync({
          name: value.name.trim(),
          target,
          cron: value.cron.trim(),
          timezone: value.timezone.trim(),
          message_template: value.messageTemplate,
        })
        onCreated?.(trigger)
        onOpenChange(false)
      } catch (err) {
        setError(errorMessage(err, 'Could not create schedule'))
      }
    },
  })

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next && createTrigger.isPending) return
        onOpenChange(next)
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add cron schedule</DialogTitle>
          <DialogDescription>
            Send a message to {targetLabel} on a recurring schedule.
          </DialogDescription>
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
            {error && <p className="text-destructive whitespace-pre-wrap text-sm">{error}</p>}
            <DialogFooter>
              <form.Subscribe
                selector={(state) =>
                  [createCronTriggerFormValid(state.values), state.isSubmitting] as const
                }
              >
                {([valid, isSubmitting]) => (
                  <Button type="submit" disabled={isSubmitting || !valid} loading={isSubmitting}>
                    Create schedule
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
