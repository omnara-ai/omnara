import type { MachineConnectionState } from '@omnara/sdk'

import { cn } from '@/lib/utils'

const connectionDotClass = {
  online: 'bg-success',
  asleep: 'bg-warning',
  offline: 'bg-muted-foreground/40',
} satisfies Record<MachineConnectionState, string>

export function MachineConnectionDot({ state }: { state: MachineConnectionState }) {
  return (
    <span
      className={cn('size-2 shrink-0 rounded-full', connectionDotClass[state])}
      title={state}
      aria-hidden="true"
    />
  )
}
