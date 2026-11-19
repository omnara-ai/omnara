import { backlogInputPreview } from '@omnara/react'
import type { AgentInput } from '@omnara/sdk'

import { type AgentScope, useScheduledMessages } from '@/components/agents/useScheduledMessages'
import { X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { formatDateTime } from '@/lib/format'
import { errorMessage } from '@/lib/submit-status'

type ScheduledInput = AgentInput & { state: 'scheduled' }

export function AgentScheduledMessages({
  canOperate,
  ...scope
}: AgentScope & { canOperate: boolean }) {
  const { scheduled, cancel } = useScheduledMessages(scope)
  return (
    <ScheduledMessagesList
      scheduled={scheduled}
      canOperate={canOperate}
      cancelPending={cancel.isPending}
      onCancel={(inputID) => {
        cancel.mutate(inputID, {
          onError: (cause) => {
            window.alert(errorMessage(cause, 'Could not cancel this scheduled message'))
          },
        })
      }}
    />
  )
}

export function ScheduledMessagesList({
  scheduled,
  canOperate,
  cancelPending,
  onCancel,
}: {
  scheduled: ScheduledInput[]
  canOperate: boolean
  cancelPending: boolean
  onCancel: (inputID: string) => void
}) {
  if (scheduled.length === 0) return null

  return (
    <ul
      aria-label="Scheduled messages"
      className="bg-background mb-2 flex flex-col divide-y rounded-md border"
    >
      {scheduled.map((input) => (
        <li key={input.id} className="flex items-center justify-between gap-3 px-3 py-2">
          <div className="flex min-w-0 flex-col gap-0.5">
            <span className="truncate text-sm font-medium">{backlogInputPreview(input).text}</span>
            <time dateTime={input.scheduled_for} className="text-muted-foreground truncate text-xs">
              Once · {formatDateTime(input.scheduled_for)}
            </time>
          </div>
          {canOperate && (
            <Button
              size="icon"
              variant="ghost"
              aria-label="Cancel scheduled message"
              className="text-muted-foreground shrink-0"
              disabled={cancelPending}
              onClick={() => {
                onCancel(input.id)
              }}
            >
              <X />
            </Button>
          )}
        </li>
      ))}
    </ul>
  )
}
