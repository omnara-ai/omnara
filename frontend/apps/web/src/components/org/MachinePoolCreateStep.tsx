import type { ReactNode } from 'react'

import { Check, ChevronDownIcon } from '@/components/icons'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

export type MachinePoolCreateStepStatus = 'done' | 'active' | 'upcoming'

/**
 * One create-pool step as a collapsible section card, matching the agent builder:
 * the header summarizes the step and toggles its fields.
 */
export function MachinePoolCreateStep({
  number,
  title,
  status,
  open,
  summary,
  optional = false,
  onOpenChange,
  children,
}: {
  number: number
  title: string
  status: MachinePoolCreateStepStatus
  open: boolean
  summary?: ReactNode
  optional?: boolean
  onOpenChange: (open: boolean) => void
  children: ReactNode
}) {
  const upcoming = status === 'upcoming'
  return (
    <Collapsible open={open} disabled={upcoming} onOpenChange={onOpenChange} asChild>
      <section
        aria-current={status === 'active' ? 'step' : undefined}
        className={cn(
          'bg-card rounded-xl border transition-colors',
          open && 'bg-muted/25',
          upcoming && 'bg-transparent',
        )}
      >
        <CollapsibleTrigger className="focus-visible:ring-ring/50 group flex min-h-14 w-full items-center gap-3 rounded-xl px-4 py-3 text-left outline-none focus-visible:ring-2 disabled:cursor-default sm:px-5">
          <StepMarker number={number} status={status} />
          <span
            data-step-title=""
            className={cn('type-label w-28 shrink-0', upcoming && 'text-muted-foreground')}
          >
            {title}
          </span>
          <span className="text-muted-foreground min-w-0 flex-1 truncate text-sm">
            {!open && status === 'done' && summary}
          </span>
          {upcoming ? (
            optional && <span className="text-muted-foreground text-sm">Optional</span>
          ) : (
            <ChevronDownIcon
              aria-hidden="true"
              className="text-muted-foreground group-hover:text-foreground size-4 shrink-0 transition-transform group-data-[state=open]:rotate-180"
            />
          )}
        </CollapsibleTrigger>
        <CollapsibleContent className="collapsible-animate-height">
          <div className="flex flex-col gap-5 border-t px-4 py-4 sm:px-5">{children}</div>
        </CollapsibleContent>
      </section>
    </Collapsible>
  )
}

function StepMarker({ number, status }: { number: number; status: MachinePoolCreateStepStatus }) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        'grid size-7 shrink-0 place-items-center rounded-full text-xs font-medium',
        status === 'done' && 'bg-primary/15 text-primary',
        status === 'active' && 'bg-primary text-primary-foreground',
        status === 'upcoming' && 'text-muted-foreground border',
      )}
    >
      {status === 'done' ? <Check className="size-3.5" strokeWidth={2.5} /> : number}
    </span>
  )
}
