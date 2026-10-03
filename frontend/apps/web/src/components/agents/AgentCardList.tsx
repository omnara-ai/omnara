import type { ComponentType, ReactNode, SVGProps } from 'react'

import { AgentIcon } from '@/components/agents/AgentIcon'
import { DataTablePagination } from '@/components/data-table/DataTable'
import { ChevronDown } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import type { PaginationControls } from '@/hooks/use-paged-query'
import type { AgentIconSpec } from '@/lib/agent-icon'
import { formatDateTime, formatTimeAgo } from '@/lib/format'
import { cn } from '@/lib/utils'

export const agentCardLinkClass =
  'truncate font-medium outline-none underline-offset-2 after:absolute after:inset-0 after:rounded-xl hover:underline'

export function AgentCardList<TItem>({
  items,
  getId,
  renderCard,
  pagination,
  isFiltered,
  isPending,
  isError,
  onRetry,
  emptyMessage,
  emptyAction,
}: {
  items: TItem[]
  getId: (item: TItem) => string
  renderCard: (item: TItem) => ReactNode
  pagination: PaginationControls
  isFiltered: boolean
  isPending: boolean
  isError: boolean
  onRetry: () => void
  emptyMessage: string
  /** Call to action shown under the empty message when the list is empty and unfiltered. */
  emptyAction?: ReactNode
}) {
  if (isPending) {
    return (
      <div className="flex flex-col gap-5">
        {[0, 1, 2].map((index) => (
          <Skeleton key={index} className="h-[7.25rem] rounded-xl" />
        ))}
      </div>
    )
  }
  if (isError) {
    return (
      <Empty className="rounded-xl border">
        <EmptyHeader>
          <EmptyDescription>Couldn&rsquo;t load this list.</EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button size="sm" variant="outline" onClick={onRetry}>
            Retry
          </Button>
        </EmptyContent>
      </Empty>
    )
  }
  if (items.length === 0) {
    return (
      <Empty className="rounded-xl border">
        <EmptyHeader>
          <EmptyDescription>{isFiltered ? 'No results.' : emptyMessage}</EmptyDescription>
        </EmptyHeader>
        {!isFiltered && emptyAction && (
          <EmptyContent className="flex-row flex-wrap justify-center gap-2">
            {emptyAction}
          </EmptyContent>
        )}
      </Empty>
    )
  }
  return (
    <div className="flex flex-col gap-3">
      <ul className="flex flex-col gap-5">
        {items.map((item) => (
          <li key={getId(item)}>{renderCard(item)}</li>
        ))}
      </ul>
      <DataTablePagination pagination={pagination} />
    </div>
  )
}

export interface AgentCardExpansion {
  id: string
  open: boolean
  content: ReactNode
}

export function AgentCard({
  icon,
  animated,
  title,
  subtitle,
  meta,
  footer,
  stats,
  expansion,
}: {
  icon: AgentIconSpec
  animated?: boolean
  title: ReactNode
  subtitle: ReactNode
  meta: ReactNode
  footer?: ReactNode
  stats?: ReactNode
  expansion?: AgentCardExpansion
}) {
  const hasBottom = footer !== undefined || stats !== undefined
  return (
    <article
      className={cn(
        'has-[a:focus-visible]:ring-ring/50 group/card relative rounded-xl border has-[a:focus-visible]:ring-[3px]',
        hasBottom ? 'bg-muted/50' : 'bg-card hover:border-foreground/20 transition-colors',
      )}
    >
      <div
        className={cn(
          'flex items-center gap-4 px-4 py-3.5',
          hasBottom &&
            'bg-card group-hover/card:border-foreground/20 -mx-px -mt-px rounded-xl border transition-colors',
        )}
      >
        <AgentIcon icon={icon} animated={animated} />
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <div className="flex min-w-0 items-center gap-2">{title}</div>
          <div className="text-muted-foreground flex min-w-0 items-center gap-1.5 text-xs">
            {subtitle}
          </div>
        </div>
        <div className="relative flex shrink-0 items-center gap-2">{meta}</div>
      </div>
      {hasBottom && (
        <div className="flex min-h-10 flex-wrap items-center justify-between gap-x-4 gap-y-1 px-4 py-2 text-xs">
          <div className="text-muted-foreground relative flex min-w-0 items-center gap-3">
            {footer}
          </div>
          <dl className="flex shrink-0 items-center gap-4">{stats}</dl>
        </div>
      )}
      {expansion && (
        <div
          id={expansion.id}
          className={cn(
            'grid transition-[grid-template-rows,opacity] duration-200 ease-out motion-reduce:transition-none',
            expansion.open ? 'grid-rows-[1fr] opacity-100' : 'grid-rows-[0fr] opacity-0',
          )}
        >
          <div className="relative min-h-0 overflow-hidden" inert={!expansion.open}>
            {expansion.content}
          </div>
        </div>
      )}
    </article>
  )
}

export function AgentCardStat({
  icon: Icon,
  label,
  value,
}: {
  icon?: ComponentType<SVGProps<SVGSVGElement>>
  label: string
  value: ReactNode
}) {
  return (
    <div className="flex items-center gap-1.5">
      {Icon && <Icon className="text-muted-foreground size-4" aria-hidden="true" />}
      <dt className="text-muted-foreground order-last">{label}</dt>
      <dd className="font-medium tabular-nums">{value ?? '—'}</dd>
    </div>
  )
}

export function AgentCardStatToggle({
  icon: Icon,
  label,
  value,
  expansion,
  onToggle,
}: {
  icon: ComponentType<SVGProps<SVGSVGElement>>
  label: string
  value: string | undefined
  expansion: AgentCardExpansion
  onToggle: () => void
}) {
  return (
    <div>
      <dt className="sr-only">{label}</dt>
      <dd>
        <button
          type="button"
          aria-expanded={expansion.open}
          aria-controls={expansion.id}
          className="hover:text-foreground relative -mx-1.5 -my-1 flex items-center gap-1.5 rounded-md px-1.5 py-1 transition-colors"
          onClick={onToggle}
        >
          <Icon className="text-muted-foreground size-4" aria-hidden="true" />
          <span className="font-medium tabular-nums">{value ?? '—'}</span>
          <span className="text-muted-foreground">{label}</span>
          <ChevronDown
            className={cn(
              'text-muted-foreground size-3.5 transition-transform duration-200 motion-reduce:transition-none',
              !expansion.open && '-rotate-90',
            )}
            aria-hidden="true"
          />
        </button>
      </dd>
    </div>
  )
}

export function AgentCardTime({
  label,
  value,
  status,
}: {
  label: string
  value: string
  /** Shown instead of the time when the state matters more than when, e.g. "Working". */
  status?: string
}) {
  return (
    <time
      dateTime={value}
      title={`${label} ${formatDateTime(value) ?? ''}`}
      className="text-muted-foreground whitespace-nowrap text-xs tabular-nums"
    >
      {status ?? formatTimeAgo(value)}
    </time>
  )
}
