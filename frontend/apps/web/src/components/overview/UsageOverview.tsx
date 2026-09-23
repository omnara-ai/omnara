import type { OrgOverviewUsage } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { type ReactNode, useState } from 'react'

import { ArrowRight } from '@/components/icons'
import { panelHintClass, tabTriggerClass } from '@/components/overview/CodeBlock'
import { OverviewSectionHeader } from '@/components/overview/OverviewSectionHeader'
import {
  formatUsageValue,
  type UsageBreakdown,
  usageChartData,
  usageMeasureValue,
} from '@/components/overview/usage-overview-data'
import { UsageChart } from '@/components/overview/UsageChart'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ReportedCost } from '@/components/usage/ReportedCost'
import { formatCount } from '@/lib/format'
import { cn } from '@/lib/utils'

const breakdowns: { value: UsageBreakdown; label: string }[] = [
  { value: 'model', label: 'Model' },
  { value: 'profile', label: 'Profile' },
]

export function UsageOverview({
  usage,
  canViewReport,
}: {
  usage: OrgOverviewUsage
  canViewReport: boolean
}) {
  const [breakdown, setBreakdown] = useState<UsageBreakdown>('model')

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
          title="Usage"
          subtitle={`Last ${usage.days.length} days`}
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
          <UsageFigures usage={usage} />
          <UsageChart
            data={usageChartData(usage, breakdown)}
            label={`Tokens per day by ${breakdown} over the last ${usage.days.length} days`}
          />
        </TabsContent>
      </Tabs>
    </section>
  )
}

function UsageFigures({ usage }: { usage: OrgOverviewUsage }) {
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
