import type { Agent, AgentActivity } from '@omnara/sdk'

import { cn } from '@/lib/utils'

const statuses: Record<AgentActivity['state'], { label: string; dotClass: string }> = {
  running: { label: 'Working', dotClass: 'bg-success animate-pulse' },
  waiting_on_interaction: { label: 'Waiting', dotClass: 'bg-warning' },
  idle: { label: 'Idle', dotClass: 'bg-muted-foreground/50' },
  archived: { label: 'Archived', dotClass: 'bg-muted-foreground/30' },
}

export function AgentStatus({
  agent,
  withSeparator,
  className,
}: {
  agent: Agent
  withSeparator?: boolean
  className?: string
}) {
  const state = agent.activity?.state ?? (agent.state === 'archived' ? 'archived' : undefined)
  if (!state) return null
  const status = statuses[state]
  return (
    <span
      className={cn(
        'text-muted-foreground inline-flex shrink-0 items-center gap-1.5 text-xs',
        className,
      )}
    >
      <span className={cn('size-1.5 rounded-full', status.dotClass)} aria-hidden="true" />
      {status.label}
      {withSeparator && (
        <span className="ml-0.5" aria-hidden="true">
          ·
        </span>
      )}
    </span>
  )
}
