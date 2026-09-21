import {
  type CronTriggerListFilters,
  useCronTriggers,
  useDeleteCronTrigger,
  useUpdateCronTrigger,
} from '@omnara/react'
import { type CronTrigger } from '@omnara/sdk'
import { type ReactNode, useState } from 'react'

import { cronTriggerDeliveryModeLabel } from '@/components/agents/cron-trigger-delivery-mode'
import { EditCronTriggerDialog } from '@/components/agents/CronTriggerDialog'
import { PauseIcon, PencilIcon, PlayIcon, Trash2 } from '@/components/icons'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { formatDateTime } from '@/lib/format'
import { errorMessage } from '@/lib/submit-status'
import { cn } from '@/lib/utils'

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

function appRunLabel(trigger: CronTrigger) {
  if (!trigger.last_run) {
    return trigger.last_fired_at ? 'Last run: details not retained' : 'Not run yet'
  }
  switch (trigger.last_run.state) {
    case 'queued':
      return 'Last run: queued'
    case 'preparing':
      return 'Last run: preparing thread'
    case 'launched':
      return 'Agent launched'
    case 'failed':
      return `Last run: ${trigger.last_run.failure_message ?? 'failed'}`
    case 'discarded':
      return 'Last run: discarded'
  }
}

interface CronTriggersListProps {
  orgId: string
  projectId: string
  canManage: boolean
  emptyMessage: string
  emptyState?: ReactNode
  /** Borderless rows for pages that lay sections out flat. */
  plain?: boolean
}

export function CronTriggersList({
  filters,
  ...props
}: CronTriggersListProps & { filters: CronTriggerListFilters }) {
  const query = useCronTriggers(props.orgId, props.projectId, { filters })
  return <CronTriggersListContent {...props} query={query} />
}

export function CronTriggersListContent({
  orgId,
  projectId,
  canManage,
  emptyMessage,
  emptyState,
  plain = false,
  query,
}: CronTriggersListProps & { query: ReturnType<typeof useCronTriggers> }) {
  const triggers = useInfiniteQueryItems(query)
  const updateTrigger = useUpdateCronTrigger(orgId, projectId)
  const deleteTrigger = useDeleteCronTrigger(orgId, projectId)
  const [editing, setEditing] = useState<CronTrigger>()

  return (
    <div className="flex flex-col gap-2">
      {triggers.length > 0 ? (
        <ul
          className={cn(
            'flex flex-col divide-y',
            plain ? 'border-y' : 'bg-background rounded-md border',
          )}
        >
          {triggers.map((trigger) => (
            <li
              key={trigger.id}
              className={cn('flex items-center justify-between gap-3 py-2', !plain && 'px-3')}
            >
              <div className="flex min-w-0 flex-col gap-0.5">
                <div className="flex min-w-0 items-center gap-2 text-sm">
                  <Tooltip>
                    <TooltipTrigger asChild>
                      {canManage ? (
                        <button
                          type="button"
                          className="truncate text-left font-medium hover:underline"
                          onClick={() => {
                            setEditing(trigger)
                          }}
                        >
                          {trigger.name}
                        </button>
                      ) : (
                        <span className="truncate font-medium">{trigger.name}</span>
                      )}
                    </TooltipTrigger>
                    <TooltipContent
                      side="bottom"
                      align="start"
                      className="max-w-sm whitespace-pre-wrap px-4 py-2 text-left text-sm leading-relaxed"
                    >
                      {trigger.message_template}
                    </TooltipContent>
                  </Tooltip>
                  {trigger.failure_report && <Badge variant="destructive">Failing</Badge>}
                </div>
                <p className="text-muted-foreground truncate text-xs">
                  <span className="font-mono">{trigger.cron}</span> · {trigger.timezone}
                  {trigger.target.type === 'agent' &&
                    ` · ${cronTriggerDeliveryModeLabel(trigger.target.delivery_mode ?? 'queued')}`}
                  {trigger.next_fire_at && nextFireLabel(trigger.next_fire_at)}
                </p>
                {trigger.target.type === 'app_launch' && (
                  <p className="text-muted-foreground break-words text-xs">
                    Channel {trigger.target.destination.channel_id} · {appRunLabel(trigger)}
                    {trigger.last_run && (
                      <>
                        {' · '}
                        <time dateTime={trigger.last_run.updated_at}>
                          {formatDateTime(trigger.last_run.updated_at)}
                        </time>
                      </>
                    )}
                  </p>
                )}
                {trigger.failure_report && (
                  <p className="text-destructive break-words text-xs">
                    Failed {formatDateTime(trigger.failure_report.failed_at)}:{' '}
                    {trigger.failure_report.message} ·{' '}
                    {trigger.failure_report.will_retry ? 'will retry' : 'will not retry'}
                  </p>
                )}
              </div>
              <div className="flex shrink-0 items-center">
                <Button
                  size="icon"
                  variant="ghost"
                  className="text-muted-foreground size-8 sm:size-7"
                  aria-label={`Edit schedule ${trigger.name}`}
                  disabled={!canManage}
                  onClick={() => {
                    setEditing(trigger)
                  }}
                >
                  <PencilIcon />
                </Button>
                <Button
                  size="icon"
                  variant="ghost"
                  className="text-muted-foreground size-8 sm:size-7"
                  aria-label={`${trigger.enabled ? 'Disable' : 'Enable'} schedule ${trigger.name}`}
                  title={trigger.enabled ? 'Disable' : 'Enable'}
                  disabled={
                    !canManage ||
                    (updateTrigger.isPending &&
                      updateTrigger.variables.cronTriggerID === trigger.id)
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
                  {trigger.enabled ? <PauseIcon /> : <PlayIcon />}
                </Button>
                <Button
                  size="icon"
                  variant="ghost"
                  className="text-muted-foreground size-8 sm:size-7"
                  aria-label={`Delete schedule ${trigger.name}`}
                  disabled={
                    !canManage ||
                    (deleteTrigger.isPending && deleteTrigger.variables === trigger.id)
                  }
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
        (emptyState ??
        (plain ? (
          <p className="text-muted-foreground text-sm">{emptyMessage}</p>
        ) : (
          <div className="border-border bg-background/60 text-muted-foreground flex min-h-16 items-center justify-center rounded-md border border-dashed px-4 text-sm">
            {emptyMessage}
          </div>
        )))
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
      {canManage && editing && (
        <EditCronTriggerDialog
          open
          onOpenChange={(open) => {
            if (!open) setEditing(undefined)
          }}
          orgId={orgId}
          projectId={projectId}
          trigger={editing}
        />
      )}
    </div>
  )
}
