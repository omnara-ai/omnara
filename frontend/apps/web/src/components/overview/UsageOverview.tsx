import { useOrgOverviewUsage } from '@omnara/react'
import type { OrgOverviewUsageResponse } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { type ReactNode, useState } from 'react'

import { ArrowRight } from '@/components/icons'
import { panelHintClass, tabTriggerClass } from '@/components/overview/CodeBlock'
import { OverviewSectionHeader } from '@/components/overview/OverviewSectionHeader'
import {
  formatUsageValue,
  usageChartData,
  usageMeasureValue,
  usageSeriesLimit,
  usageWindowDays,
  usageWindowSince,
} from '@/components/overview/usage-overview-data'
import { UsageChart } from '@/components/overview/UsageChart'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ReportedCost } from '@/components/usage/ReportedCost'
import { formatCount } from '@/lib/format'
import { errorMessage } from '@/lib/submit-status'
import { cn } from '@/lib/utils'

type UsageBreakdown = 'model' | 'profile'

const breakdowns: { value: UsageBreakdown; label: string }[] = [
  { value: 'model', label: 'Model' },
  { value: 'profile', label: 'Profile' },
]

export function UsageOverview({ orgId, canViewReport }: { orgId: string; canViewReport: boolean }) {
  const [since] = useState(() => usageWindowSince(new Date()).toISOString())
  const [timezone] = useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone)
  const [breakdown, setBreakdown] = useState<UsageBreakdown>('model')
  const query = useOrgOverviewUsage(orgId, {
    since,
    interval: 'day',
    timezone,
    groupBy: breakdown,
    limit: usageSeriesLimit,
  })

  return (
    <section>
      <Tabs
        value={breakdown}
        onValueChange={(value) => {
          const next = breakdowns.find((option) => option.value === value)
          if (next) setBreakdown(next.value)
        }}
        className="gap-6"
      >
        <OverviewSectionHeader
          title="Monthly usage"
          action={
            <div className="flex items-center gap-4">
              <TabsList variant="line" aria-label="Break usage down by" className="gap-1 p-0">
                {breakdowns.map((option) => (
                  <TabsTrigger key={option.value} value={option.value} className={tabTriggerClass}>
                    {option.label}
                  </TabsTrigger>
                ))}
              </TabsList>
              {canViewReport && (
                <Link to="/usage" className={cn(panelHintClass, 'inline-flex hover:underline')}>
                  Usage report
                  <ArrowRight className="size-3.5" aria-hidden="true" />
                </Link>
              )}
            </div>
          }
        />
        <TabsContent value={breakdown} className="flex flex-col gap-6">
          {query.isPending ? (
            <UsageSkeleton />
          ) : query.isError ? (
            <p className="text-destructive text-sm" role="alert">
              {errorMessage(query.error, 'Could not load usage.')}
            </p>
          ) : (
            <>
              <UsageFigures usage={query.data} />
              <div
                aria-busy={query.isPlaceholderData}
                className={cn('transition-opacity', query.isPlaceholderData && 'opacity-50')}
              >
                <UsageChart
                  data={usageChartData(query.data)}
                  label={`Tokens per day by ${breakdown} over the last ${usageWindowDays} days`}
                />
              </div>
            </>
          )}
        </TabsContent>
      </Tabs>
    </section>
  )
}

function UsageFigures({ usage }: { usage: OrgOverviewUsageResponse }) {
  return (
    <div className="grid grid-cols-2 gap-x-6 gap-y-3 pb-3 sm:flex sm:flex-wrap sm:items-baseline sm:gap-x-10">
      <UsageFigure
        value={formatUsageValue('tokens', usageMeasureValue(usage.totals, 'tokens'))}
        label="tokens"
      />
      <UsageFigure
        value={
          <ReportedCost
            modelCalls={usage.totals.model_calls}
            cost={usage.totals.cost}
            format={(decimal) => formatUsageValue('cost', Number(decimal))}
          />
        }
        label="cost"
      />
      <UsageFigure value={formatCount(usage.totals.model_calls)} label="model calls" />
      <UsageFigure value={formatCount(usage.active_agents)} label="active agents" />
    </div>
  )
}

function UsageFigure({ value, label }: { value: ReactNode; label: string }) {
  return (
    <p className="flex min-w-0 flex-wrap items-baseline gap-x-2">
      <span className="text-xl font-semibold tracking-[-0.03em] sm:text-3xl">{value}</span>
      <span className="text-muted-foreground text-sm">{label}</span>
    </p>
  )
}

function UsageSkeleton() {
  return (
    <div className="flex flex-col gap-6">
      <Skeleton className="h-9 w-full max-w-2xl" />
      <Skeleton className="h-56 sm:h-72" />
    </div>
  )
}
