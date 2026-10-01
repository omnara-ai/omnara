import type { ModelUsageTotals, UsageReport as UsageReportData, UsageTotals } from '@omnara/sdk'
import type { UseQueryResult } from '@tanstack/react-query'
import type { ReactNode } from 'react'

import { Empty, EmptyContent, EmptyDescription, EmptyHeader } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { ReportedCost } from '@/components/usage/ReportedCost'
import { type UsageStat, UsageStats, UsageStatsSkeleton } from '@/components/usage/UsageStats'
import { formatCount } from '@/lib/format'
import { errorMessage } from '@/lib/submit-status'

export function UsageReportView({
  query,
  emptyMessage = 'No model usage recorded yet.',
  emptyAction,
  chart,
}: {
  query: UseQueryResult<UsageReportData, unknown>
  emptyMessage?: string
  /** Call to action shown under the empty message when no usage is recorded. */
  emptyAction?: ReactNode
  /** Usage over time, shown between the summary and the per-model table when usage exists. */
  chart?: ReactNode
}) {
  if (query.isPending) {
    return (
      <div className="flex flex-col gap-4">
        <Skeleton className="h-56 sm:h-72" />
        <UsageStatsSkeleton />
        <Skeleton className="h-40" />
      </div>
    )
  }
  if (query.isError) {
    return (
      <p className="text-destructive text-sm" role="alert">
        {errorMessage(query.error, 'Could not load usage.')}
      </p>
    )
  }
  const report = query.data
  if (report.totals.model_calls === 0) {
    return (
      <Empty className="rounded-xl border">
        <EmptyHeader>
          <EmptyDescription>{emptyMessage}</EmptyDescription>
        </EmptyHeader>
        {emptyAction && <EmptyContent>{emptyAction}</EmptyContent>}
      </Empty>
    )
  }
  return (
    <div className="flex flex-col gap-6">
      {chart}
      <UsageStats stats={usageSummaryStats(report.totals)} />
      <UsageByModelTable rows={report.by_model} />
    </div>
  )
}

function usageSummaryStats(totals: UsageTotals): UsageStat[] {
  return [
    {
      label: 'Provider-reported cost',
      value: <ReportedCost modelCalls={totals.model_calls} cost={totals.cost} />,
    },
    { label: 'Input tokens', value: formatCount(totals.tokens.input_tokens_total) },
    { label: 'Output tokens', value: formatCount(totals.tokens.output_tokens_total) },
    { label: 'Model calls', value: formatCount(totals.model_calls) },
  ]
}

function UsageByModelTable({ rows }: { rows: ModelUsageTotals[] }) {
  return (
    <div className="overflow-x-auto rounded-lg border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Model</TableHead>
            <TableHead className="text-right">Calls</TableHead>
            <TableHead className="text-right">Uncached input</TableHead>
            <TableHead className="text-right">Cache read</TableHead>
            <TableHead className="text-right">Cache write</TableHead>
            <TableHead className="text-right">Output</TableHead>
            <TableHead className="text-right">Reasoning</TableHead>
            <TableHead className="pr-4 text-right">Cost</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row) => (
            <TableRow key={`${row.model.configured_model_id}:${row.model.provider_model_slug}`}>
              <TableCell>
                <div className="flex min-w-0 flex-col">
                  <span className="truncate">{row.model.name}</span>
                  <span className="text-muted-foreground truncate font-mono text-xs">
                    {row.model.model_provider_config_name} · {row.model.provider_model_slug}
                  </span>
                </div>
              </TableCell>
              <TableCell className="text-right tabular-nums">
                {formatCount(row.model_calls)}
              </TableCell>
              <TableCell className="text-right tabular-nums">
                {formatCount(row.tokens.uncached_input_tokens)}
              </TableCell>
              <TableCell className="text-right tabular-nums">
                {formatCount(row.tokens.cache_read_input_tokens)}
              </TableCell>
              <TableCell className="text-right tabular-nums">
                {formatCount(row.tokens.cache_write_input_tokens)}
              </TableCell>
              <TableCell className="text-right tabular-nums">
                {formatCount(row.tokens.output_tokens_total)}
              </TableCell>
              <TableCell className="text-right tabular-nums">
                {formatCount(row.tokens.reasoning_output_tokens)}
              </TableCell>
              <TableCell className="pr-4 text-right tabular-nums">
                <ReportedCost modelCalls={row.model_calls} cost={row.cost} />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}
