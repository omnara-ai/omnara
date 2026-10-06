import type { ReactNode } from 'react'

import { Check, ChevronDownIcon } from '@/components/icons'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

export type MachinePoolCreateStepStatus = 'done' | 'active' | 'upcoming'

/**
 * One create-pool step as a collapsible section card in the agent builder's style:
 * a compact header that summarizes the step and toggles its fields.
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
    <Collapsible open={open} onOpenChange={onOpenChange} asChild>
      <section
        aria-current={status === 'active' ? 'step' : undefined}
        className="bg-card rounded-xl border"
      >
        <CollapsibleTrigger className="focus-visible:ring-ring/50 group flex w-full items-center gap-3 rounded-xl px-4 py-3 text-left outline-none focus-visible:ring-2 sm:px-5">
          <span
            aria-hidden="true"
            className={cn(
              'grid w-4 shrink-0 place-items-center text-xs tabular-nums',
              status === 'done' ? 'text-primary' : 'text-muted-foreground',
            )}
          >
            {status === 'done' ? <Check className="size-4" strokeWidth={2.5} /> : number}
          </span>
          <span
            data-step-title=""
            className={cn('type-label shrink-0', upcoming && 'text-muted-foreground')}
          >
            {title}
          </span>
          <span className="text-muted-foreground min-w-0 flex-1 truncate text-sm">
            {!open && status === 'done' && summary}
          </span>
          {optional && upcoming && !open && (
            <span className="text-muted-foreground text-sm">Optional</span>
          )}
          <span className="grid size-8 shrink-0 place-items-center">
            <ChevronDownIcon
              aria-hidden="true"
              className="text-muted-foreground group-hover:text-foreground size-4 transition-transform group-data-[state=open]:rotate-180"
            />
          </span>
        </CollapsibleTrigger>
        <CollapsibleContent className="collapsible-animate-height">
          <div className="flex flex-col gap-4 px-4 pb-5 pt-1 sm:px-5">{children}</div>
        </CollapsibleContent>
      </section>
    </Collapsible>
  )
}
