import type { ReactNode } from 'react'

import { formatTimeAgo } from '@/lib/format'

// A few rows each, so the lists stay quiet next to the usage below.
export const overviewRowLimit = 4
/** A row link: no card around the list, so each row lights up on hover instead. */
export const overviewRowLinkClass =
  'group flex min-w-0 items-center gap-2.5 rounded-lg px-2 py-1.5 text-sm transition-colors hover:bg-foreground/[0.04]'

/** A quiet labelled list, so the column stays out of the way of what the page leads with. */
export function OverviewColumn({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex min-w-0 flex-col gap-1.5">
      <h3 className="text-muted-foreground px-2 text-xs font-medium">{title}</h3>
      {children}
    </section>
  )
}

export function OverviewEmpty({ message, action }: { message: string; action?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-3 px-2 py-1.5">
      <p className="text-muted-foreground text-sm">{message}</p>
      {action}
    </div>
  )
}

export function OverviewList({ children }: { children: ReactNode }) {
  return <ul className="flex flex-col">{children}</ul>
}

export function OverviewRowLabel({ name }: { name: string }) {
  return (
    <span className="text-foreground/85 group-hover:text-foreground min-w-0 flex-1 truncate">
      {name}
    </span>
  )
}

export function OverviewRowTime({ value }: { value: string }) {
  return (
    <span className="text-muted-foreground w-16 shrink-0 text-right text-xs tabular-nums">
      {formatTimeAgo(value)}
    </span>
  )
}
