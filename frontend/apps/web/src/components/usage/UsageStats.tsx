import type { ReactNode } from 'react'

import { Skeleton } from '@/components/ui/skeleton'

export interface UsageStat {
  label: string
  value: ReactNode
}

/** The headline usage numbers, shown as one bordered row of tiles under a usage chart. */
export function UsageStats({ stats }: { stats: UsageStat[] }) {
  return (
    <dl className="bg-border grid grid-cols-2 gap-px overflow-hidden rounded-xl border sm:grid-cols-4">
      {stats.map((stat) => (
        <div key={stat.label} className="bg-card flex min-w-0 flex-col gap-1 px-4 py-3">
          <dt className="text-muted-foreground truncate text-xs">{stat.label}</dt>
          <dd className="truncate text-xl font-semibold tabular-nums tracking-tight">
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
