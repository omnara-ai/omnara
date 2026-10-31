import type { ReactNode } from 'react'

import { Card } from '@/components/ui/card'
import { formatTimeAgo } from '@/lib/format'
import { cn } from '@/lib/utils'

export const overviewRowLimit = 5
export const overviewRowLinkClass = 'group flex h-full min-w-0 items-center gap-3 text-sm'
/** A row of a plain column: no card around it, so each row lights up on hover instead. */
export const overviewPlainRowLinkClass =
  'group flex min-w-0 items-center gap-2.5 rounded-lg px-2 py-1.5 text-sm transition-colors hover:bg-foreground/[0.04]'

/**
 * A titled overview card. `plain` drops the card for a quiet labelled list, so the column
 * stays out of the way of what the page leads with.
 */
export function OverviewColumn({
  title,
  plain = false,
  children,
}: {
  title: string
  plain?: boolean
  children: ReactNode
}) {
  if (plain) {
    return (
      <section className="flex min-w-0 flex-col gap-1.5">
        <h3 className="text-muted-foreground px-2 text-xs font-medium">{title}</h3>
        {children}
      </section>
    )
  }
  return (
    <Card className="min-w-0 gap-4 p-5">
      <h3 className="text-foreground/80 flex h-6 items-center text-sm font-medium">{title}</h3>
      {children}
    </Card>
  )
}

export function OverviewEmpty({
  message,
  action,
  plain = false,
}: {
  message: string
  action?: ReactNode
  plain?: boolean
}) {
  if (plain) return <p className="text-muted-foreground px-2 py-1.5 text-sm">{message}</p>
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 py-6 text-center">
      <p className="text-muted-foreground text-sm">{message}</p>
      {action}
    </div>
  )
}

export function OverviewList({
  plain = false,
  children,
}: {
  plain?: boolean
  children: ReactNode
}) {
  return (
    <ul className={cn(plain ? 'flex flex-col' : 'grid flex-1 grid-rows-5 gap-1.5')}>{children}</ul>
  )
}

export function OverviewRowLabel({
  name,
  subtitle,
  plain = false,
}: {
  name: string
  subtitle?: string
  plain?: boolean
}) {
  if (plain) {
    return (
      <span className="text-foreground/85 group-hover:text-foreground min-w-0 flex-1 truncate">
        {name}
      </span>
    )
  }
  return (
    <span className="flex min-w-0 flex-1 flex-col">
      <span className="truncate underline-offset-2 group-hover:underline">{name}</span>
      {subtitle && <span className="text-muted-foreground truncate text-xs">{subtitle}</span>}
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
