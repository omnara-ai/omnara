import type { ReactNode } from 'react'

import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'

export interface UsageStat {
  label: string
  info?: ReactNode
  value: ReactNode
}

/**
 * The headline usage numbers, shown as one bordered row of tiles under a usage chart.
 * `plain` drops the tiles for a row of figures, for a chart already inside a panel.
 */
export function UsageStats({
  stats,
  plain = false,
  narrow = false,
}: {
  stats: UsageStat[]
  plain?: boolean
  /** Keeps two figures per row at every width, for a narrow column. */
  narrow?: boolean
}) {
  return (
    <dl
      className={cn(
        'grid grid-cols-2',
        !narrow && 'sm:grid-cols-4',
        plain
          ? 'border-foreground/[0.06] gap-x-6 gap-y-4 border-t pt-5'
          : 'bg-border gap-px overflow-hidden rounded-xl border',
      )}
    >
      {stats.map((stat) => (
        <div
          key={stat.label}
          className={cn('flex min-w-0 flex-col gap-1', !plain && 'bg-card px-4 py-3')}
        >
          <dt className="text-muted-foreground flex min-w-0 items-center gap-1 text-xs">
            <span className="truncate">{stat.label}</span>
            {stat.info}
          </dt>
          <dd className="truncate text-xl font-semibold tracking-tight tabular-nums">
            {stat.value}
          </dd>
        </div>
      ))}
    </dl>
  )
}

export function UsageStatsSkeleton() {
  return <Skeleton className="h-[8.5rem] rounded-xl sm:h-[4.25rem]" />
}
