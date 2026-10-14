import type { ReactNode } from 'react'

import { Card } from '@/components/ui/card'
import { formatTimeAgo } from '@/lib/format'

export const overviewRowLimit = 5
export const overviewRowLinkClass = 'group flex h-full min-w-0 items-center gap-3 text-sm'

export function OverviewColumn({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Card className="min-w-0 gap-4 p-5">
      <h3 className="text-foreground/80 flex h-6 items-center text-sm font-medium">{title}</h3>
      {children}
    </Card>
  )
}

export function OverviewEmpty({ message, action }: { message: string; action?: ReactNode }) {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 py-6 text-center">
      <p className="text-muted-foreground text-sm">{message}</p>
      {action}
    </div>
  )
}

export function OverviewList({ children }: { children: ReactNode }) {
  return <ul className="grid flex-1 grid-rows-5 gap-1.5">{children}</ul>
}

export function OverviewRowLabel({ name, subtitle }: { name: string; subtitle?: string }) {
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
