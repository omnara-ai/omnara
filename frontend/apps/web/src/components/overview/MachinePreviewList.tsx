import type { MachineSummary } from '@omnara/sdk'
import type { ReactNode } from 'react'

import { MachineConnectionDot } from '@/components/overview/MachineConnectionDot'
import { machineStatusLabel } from '@/components/overview/machineFormat'
import { formatDateTime, formatTimeAgo } from '@/lib/format'

/** The first few machines of a pool (or BYO), shown inside an expanded card. */
export function MachinePreviewList({
  machines,
  isPending,
  isError,
  emptyMessage,
  emptyAction,
  viewAll,
}: {
  machines: MachineSummary[]
  isPending: boolean
  isError: boolean
  emptyMessage: string
  /** Shown under the empty message, e.g. a button to add the first machine. */
  emptyAction?: ReactNode
  /** Link to the full list; omit when every machine is already shown. */
  viewAll?: ReactNode
}) {
  return (
    <div className="flex flex-col gap-1 border-t px-2 py-2">
      {isError ? (
        <p className="text-muted-foreground px-2 py-1.5 text-sm">Couldn&rsquo;t load machines.</p>
      ) : machines.length === 0 ? (
        <div className="flex flex-col items-start gap-2 px-2 py-1.5">
          <p className="text-muted-foreground text-sm">
            {isPending ? 'Loading machines…' : emptyMessage}
          </p>
          {!isPending && emptyAction}
        </div>
      ) : (
        <ul className="flex flex-col gap-0.5">
          {machines.map((machine) => {
            const lastSeen = machine.last_observed_at ?? machine.updated_at
            return (
              <li
                key={machine.id}
                className="flex min-h-9 min-w-0 items-center gap-2.5 rounded-md px-2 py-1.5 text-sm"
              >
                <MachineConnectionDot state={machine.connection_state} />
                <span className="truncate">{machine.display_name}</span>
                <span className="text-muted-foreground shrink-0 text-xs capitalize">
                  {machineStatusLabel(machine)}
                </span>
                <span
                  className="text-muted-foreground ml-auto shrink-0 text-xs tabular-nums"
                  title={`Last seen ${formatDateTime(lastSeen) ?? ''}`}
                >
                  {formatTimeAgo(lastSeen)}
                </span>
              </li>
            )
          })}
        </ul>
      )}
      {viewAll && <div className="flex justify-end px-1">{viewAll}</div>}
    </div>
  )
}
