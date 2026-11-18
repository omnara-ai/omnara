import { backlogInputPreview, isScheduledBacklogInput, useAgentInputBacklog } from '@omnara/react'

import { X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { formatDateTime } from '@/lib/format'
import { errorMessage } from '@/lib/submit-status'

export function AgentScheduledMessages({
  orgId,
  projectId,
  agentId,
  canManage,
}: {
  orgId: string
  projectId: string
  agentId: string
  canManage: boolean
}) {
  const backlog = useAgentInputBacklog({ orgID: orgId, projectID: projectId, agentID: agentId })
  const scheduled = (backlog.query.data?.data ?? []).filter(isScheduledBacklogInput)
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
          {canManage && (
            <Button
              size="icon"
              variant="ghost"
              aria-label="Cancel scheduled message"
              className="text-muted-foreground shrink-0"
              disabled={backlog.cancel.isPending}
              onClick={() => {
                backlog.cancel.mutate(input.id, {
                  onError: (cause) => {
                    window.alert(errorMessage(cause, 'Could not cancel this scheduled message'))
                  },
                })
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
