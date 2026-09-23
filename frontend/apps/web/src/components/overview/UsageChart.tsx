import { type KeyboardEvent, useState } from 'react'

import {
  formatDayLabel,
  formatDayTitle,
  formatUsageValue,
  labelledColumns,
  type UsageChartData,
  type UsageColumn,
  type UsageSeries,
  usageTicks,
} from '@/components/overview/usage-overview-data'
import { formatCompactCount } from '@/lib/format'
import { cn } from '@/lib/utils'

const plotHeightClass = 'h-56 sm:h-72'
const maxAxisLabels = 7
const maxCompactAxisLabels = 4
const barWidth = 'min(72px, calc(100% - max(2px, 16%)))'
const tooltipWidth = '14rem'
const inProgressHatch =
  'repeating-linear-gradient(135deg, color-mix(in oklab, var(--muted-foreground) 32%, transparent) 0 1px, transparent 1px 5px)'

export function UsageChart({ data, label }: { data: UsageChartData; label: string }) {
  const [focusedIndex, setFocusedIndex] = useState<number | null>(null)
  const count = data.columns.length
  const ticks = usageTicks(Math.max(0, ...data.columns.map((column) => column.total)))
  const top = ticks.at(-1) ?? 0
  const labelled = labelledColumns(count, maxAxisLabels)
  const compactLabelled = labelledColumns(count, maxCompactAxisLabels)
  const focusedColumn = focusedIndex === null ? undefined : data.columns[focusedIndex]
  const share = (value: number) => (top > 0 ? `${(value / top) * 100}%` : '0%')

  function moveFocus(event: KeyboardEvent<HTMLDivElement>) {
    const current = focusedIndex ?? count - 1
    const next =
      event.key === 'ArrowRight'
        ? Math.min(count - 1, current + 1)
        : event.key === 'ArrowLeft'
          ? Math.max(0, current - 1)
          : event.key === 'Home'
            ? 0
            : event.key === 'End'
              ? count - 1
              : undefined
    if (next === undefined || count === 0) return
    event.preventDefault()
    setFocusedIndex(next)
  }

  return (
    <div className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3">
      <div aria-hidden="true" className={cn('relative w-11', plotHeightClass)}>
        {ticks
          .filter((tick) => tick > 0)
          .map((tick) => (
            <span
              key={tick}
              className="text-muted-foreground absolute right-0 translate-y-1/2 text-xs tabular-nums leading-none"
              style={{ bottom: share(tick) }}
            >
              {formatCompactCount(tick)}
            </span>
          ))}
      </div>
      <div
        role="slider"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={Math.max(0, count - 1)}
        aria-valuenow={focusedIndex ?? Math.max(0, count - 1)}
        aria-valuetext={columnSummary(data.columns[focusedIndex ?? count - 1])}
        tabIndex={0}
        className={cn(
          'focus-visible:ring-ring/50 relative rounded-sm outline-none focus-visible:ring-2',
          plotHeightClass,
        )}
        onKeyDown={moveFocus}
        onFocus={() => {
          setFocusedIndex((current) => current ?? count - 1)
        }}
        onBlur={() => {
          setFocusedIndex(null)
        }}
        onPointerLeave={() => {
          setFocusedIndex(null)
        }}
      >
        <div aria-hidden="true" className="absolute inset-0 flex">
          {data.columns.map((column, index) => (
            <div
              key={column.start.toISOString()}
              className="relative h-full min-w-0 flex-1"
              onPointerEnter={() => {
                setFocusedIndex(index)
              }}
            >
              {focusedIndex === index && <span className="bg-foreground/[0.06] absolute inset-0" />}
              <UsageColumnBar
                column={column}
                series={data.series}
                top={top}
                inProgress={index === count - 1}
              />
            </div>
          ))}
        </div>
        {top === 0 && (
          <span className="text-muted-foreground absolute inset-0 flex items-center justify-center text-sm">
            No usage
          </span>
        )}
        {focusedColumn && focusedIndex !== null && (
          <UsageTooltip
            column={focusedColumn}
            series={data.series}
            index={focusedIndex}
            count={count}
          />
        )}
      </div>
      <span />
      <div aria-hidden="true" className="relative h-8">
        {data.columns.map((column, index) =>
          labelled.has(index) || compactLabelled.has(index) ? (
            <span
              key={column.start.toISOString()}
              className={cn(
                'text-muted-foreground absolute top-3 whitespace-nowrap text-xs tabular-nums leading-none',
                index === count - 1 ? '-translate-x-full' : '-translate-x-1/2',
                !compactLabelled.has(index) && 'max-sm:hidden',
                !labelled.has(index) && 'sm:hidden',
              )}
              style={{
                left: index === count - 1 ? '100%' : `${((index + 0.5) / count) * 100}%`,
              }}
            >
              {formatDayLabel(column.start)}
            </span>
          ) : null,
        )}
      </div>
    </div>
  )
}

function columnSummary(column: UsageColumn | undefined) {
  if (!column) return undefined
  return `${formatDayTitle(column.start)}: ${formatUsageValue('tokens', column.total)} tokens`
}

function UsageColumnBar({
  column,
  series,
  top,
  inProgress,
}: {
  column: UsageColumn
  series: UsageSeries[]
  top: number
  inProgress: boolean
}) {
  if (top === 0) return null
  const filled = (Math.max(0, column.total) / top) * 100
  return (
    <div className="absolute inset-y-0 left-1/2 -translate-x-1/2" style={{ width: barWidth }}>
      {inProgress && (
        <span
          className="absolute inset-x-0 top-0"
          style={{ bottom: `${filled}%`, backgroundImage: inProgressHatch }}
        />
      )}
      {stackSegments(column, series).map((segment) => (
        <span
          key={segment.key}
          className="absolute inset-x-0"
          style={{
            backgroundColor: segment.color,
            bottom: `${(segment.from / top) * 100}%`,
            height: `${(segment.value / top) * 100}%`,
          }}
        />
      ))}
    </div>
  )
}

function stackSegments(column: UsageColumn, series: UsageSeries[]) {
  const segments: { key: string; color: string; from: number; value: number }[] = []
  let from = 0
  for (const entry of series) {
    const value = column.values.get(entry.key) ?? 0
    if (value <= 0) continue
    segments.push({ key: entry.key, color: entry.color, from, value })
    from += value
  }
  return segments
}

function UsageTooltip({
  column,
  series,
  index,
  count,
}: {
  column: UsageColumn
  series: UsageSeries[]
  index: number
  count: number
}) {
  const rows = series
    .flatMap((entry) => {
      const value = column.values.get(entry.key) ?? 0
      return value > 0 ? [{ ...entry, value }] : []
    })
    .sort((left, right) => right.value - left.value)
  const onLeft = (index + 0.5) / count > 0.5
  const left = onLeft
    ? `max(0px, calc(${(index / count) * 100}% - 8px - ${tooltipWidth}))`
    : `min(calc(${((index + 1) / count) * 100}% + 8px), calc(100% - ${tooltipWidth}))`
  return (
    <div
      aria-hidden="true"
      className="bg-popover text-popover-foreground pointer-events-none absolute top-0 z-10 rounded-lg border px-3 py-2.5 shadow-lg"
      style={{ left, width: tooltipWidth }}
    >
      <p className="text-muted-foreground mb-2 text-xs">{formatDayTitle(column.start)}</p>
      {rows.length > 0 && (
        <ul className="mb-2 flex flex-col gap-1.5 border-b pb-2">
          {rows.map((row) => (
            <li key={row.key} className="flex items-center gap-2 text-xs">
              <span
                className="h-3.5 w-1 shrink-0 rounded-full"
                style={{ backgroundColor: row.color }}
              />
              <span className="min-w-0 flex-1 truncate">{row.name}</span>
              <span className="tabular-nums">{formatUsageValue('tokens', row.value)}</span>
            </li>
          ))}
        </ul>
      )}
      <p className="flex items-center justify-between gap-2 text-xs font-medium">
        <span>Total</span>
        <span className="tabular-nums">{formatUsageValue('tokens', column.total)}</span>
      </p>
    </div>
  )
}
