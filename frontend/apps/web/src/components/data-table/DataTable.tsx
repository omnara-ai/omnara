import type { ReactNode } from 'react'
import { useState } from 'react'

import { useColumnResizing } from '@/components/data-table/column-resizing'
import { Button } from '@/components/ui/button'
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
import type { PaginationControls } from '@/hooks/use-paged-query'
import { cn } from '@/lib/utils'

export interface DataTableColumn<TData> {
  id: string
  header: string
  cell: (item: TData) => ReactNode
  /** Classes applied to both the header and body cells of this column. */
  className?: string
  /** Right-aligns the cell and keeps clicks inside it from triggering the row. */
  isActions?: boolean
  /** Set false to keep an actions cell always visible instead of fading in on row hover. */
  revealOnHover?: boolean
}

export function DataTable<TData>({
  columns,
  data,
  getRowId,
  rowExpanded,
  onRowClick,
  pagination,
  isFiltered,
  isPending,
  isError,
  onRetry,
  emptyMessage,
}: {
  columns: readonly DataTableColumn<TData>[]
  /** The rows of the current page. Paging and filtering happen in the caller. */
  data: TData[]
  getRowId: (item: TData) => string
  /** Detail panel toggled open by clicking the row. Mutually exclusive with onRowClick. */
  rowExpanded?: (item: TData) => ReactNode
  /** Row click handler for rows that navigate instead of expanding. */
  onRowClick?: (item: TData) => void
  /** Cursor-style paging controls (see usePagedQuery / useArrayPagination). */
  pagination?: PaginationControls
  /** Whether the caller's search is narrowing `data`, so an empty table means "no results". */
  isFiltered?: boolean
  isPending?: boolean
  isError?: boolean
  onRetry?: () => void
  emptyMessage: string
}) {
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set())
  const resizing = useColumnResizing(columns)
  const columnCount = columns.length
  const hasDataRows = !isPending && !isError && data.length > 0

  function toggleExpanded(id: string) {
    setExpanded((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  return (
    <section className="flex flex-col gap-3">
      <div className="@container overflow-hidden rounded-xl border">
        <div
          className={cn(
            'after:border-border/50 after:bg-muted/50 after:shadow-xs relative isolate after:pointer-events-none after:absolute after:-inset-x-px after:-top-px after:-z-10 after:h-[calc(2.25rem+2px)] after:rounded-xl after:border',
            !hasDataRows && 'max-md:after:hidden',
          )}
        >
          <Table className={cn('table-fixed lg:min-w-0', hasDataRows && 'min-w-[40rem]')}>
            {hasDataRows && (
              <colgroup>
                {columns.map((column, index) => (
                  <col
                    key={column.id}
                    style={
                      resizing.columnWidths ? { width: resizing.columnWidths[index] } : undefined
                    }
                  />
                ))}
              </colgroup>
            )}
            <TableHeader
              className={cn(
                'bg-transparent [&_tr]:border-0',
                !hasDataRows && 'hidden md:table-header-group',
              )}
            >
              <DataTableHeaderRow columns={columns} resizing={resizing} />
            </TableHeader>
            <TableBody>
              {isPending || isError || data.length === 0 ? (
                <DataTableStateRows
                  columnCount={columnCount}
                  isPending={isPending}
                  isError={isError}
                  isFiltered={isFiltered}
                  onRetry={onRetry}
                  emptyMessage={emptyMessage}
                />
              ) : (
                data.map((item) => {
                  const id = getRowId(item)
                  return (
                    <DataRow
                      key={id}
                      item={item}
                      columns={columns}
                      expanded={rowExpanded ? expanded.has(id) : false}
                      rowExpanded={rowExpanded}
                      onClick={
                        rowExpanded
                          ? () => {
                              toggleExpanded(id)
                            }
                          : onRowClick
                            ? () => {
                                onRowClick(item)
                              }
                            : undefined
                      }
                    />
                  )
                })
              )}
            </TableBody>
          </Table>
        </div>
        {pagination && <DataTablePagination pagination={pagination} />}
      </div>
    </section>
  )
}

function DataTableStateRows({
  columnCount,
  isPending,
  isError,
  isFiltered,
  onRetry,
  emptyMessage,
}: {
  columnCount: number
  isPending?: boolean
  isError?: boolean
  isFiltered?: boolean
  onRetry?: () => void
  emptyMessage: string
}) {
  if (isPending) {
    return [0, 1].map((index) => (
      <TableRow key={index} className="h-12 hover:bg-transparent">
        <TableCell colSpan={columnCount} className="px-4">
          <Skeleton className={index === 0 ? 'h-4 w-40' : 'h-4 w-28'} />
        </TableCell>
      </TableRow>
    ))
  }
  if (isError) {
    return (
      <StateRow columnCount={columnCount}>
        <Empty>
          <EmptyHeader>
            <EmptyDescription>Couldn&rsquo;t load this list.</EmptyDescription>
          </EmptyHeader>
          {onRetry && (
            <EmptyContent>
              <Button size="sm" variant="outline" onClick={onRetry}>
                Retry
              </Button>
            </EmptyContent>
          )}
        </Empty>
      </StateRow>
    )
  }
  return (
    <StateRow columnCount={columnCount}>
      <Empty>
        <EmptyHeader>
          <EmptyDescription>{isFiltered ? 'No results.' : emptyMessage}</EmptyDescription>
        </EmptyHeader>
      </Empty>
    </StateRow>
  )
}

function DataTablePagination({ pagination }: { pagination: PaginationControls }) {
  if (!pagination.canPrev && !pagination.canNext && pagination.page === 0) return null
  return (
    <div className="bg-muted/50 flex items-center justify-end gap-3 rounded-xl px-4 py-1.5">
      <span className="text-muted-foreground/70 text-xs tabular-nums">
        Page {pagination.page + 1}
      </span>
      <Button
        type="button"
        size="sm"
        variant="ghost"
        className="text-muted-foreground hover:text-foreground h-9 px-2 text-xs sm:h-6 sm:px-1.5"
        disabled={!pagination.canPrev}
        onClick={pagination.onPrev}
      >
        Previous
      </Button>
      <Button
        type="button"
        size="sm"
        variant="ghost"
        className="text-muted-foreground hover:text-foreground h-9 px-2 text-xs sm:h-6 sm:px-1.5"
        disabled={!pagination.canNext}
        onClick={pagination.onNext}
      >
        Next
      </Button>
    </div>
  )
}

function DataTableHeaderRow<TData>({
  columns,
  resizing,
}: {
  columns: readonly DataTableColumn<TData>[]
  resizing: ReturnType<typeof useColumnResizing>
}) {
  const columnCount = columns.length
  return (
    <TableRow className="hover:bg-transparent">
      {columns.map((column, index) => (
        <TableHead
          key={column.id}
          className={cn('relative h-[calc(2.25rem+3px)] px-4 pb-[3px]', column.className)}
        >
          {column.header}
          {index < columnCount - 1 && (
            <button
              type="button"
              aria-label={`Resize ${column.header || 'actions'} column`}
              className={cn(
                'after:bg-border hover:after:bg-ring focus-visible:after:bg-ring absolute inset-y-0 -right-1 z-20 hidden w-2 cursor-col-resize touch-none select-none border-0 bg-transparent p-0 outline-none after:absolute after:inset-y-2 after:left-1/2 after:w-px after:transition-colors lg:block',
                resizing.resizingColumn === index && 'after:bg-ring',
              )}
              onPointerDown={(event) => {
                resizing.begin(index, event)
              }}
              onPointerMove={resizing.move}
              onPointerUp={resizing.end}
              onPointerCancel={resizing.end}
              onKeyDown={(event) => {
                resizing.resizeWithKeyboard(index, event)
              }}
              onDoubleClick={resizing.reset}
            />
          )}
        </TableHead>
      ))}
    </TableRow>
  )
}

function DataRow<TData>({
  item,
  columns,
  expanded,
  rowExpanded,
  onClick,
}: {
  item: TData
  columns: readonly DataTableColumn<TData>[]
  expanded: boolean
  rowExpanded?: (item: TData) => ReactNode
  onClick?: () => void
}) {
  return (
    <>
      <TableRow
        className={cn('group/row h-11', onClick && 'cursor-pointer', expanded && 'border-b-0')}
        onClick={onClick}
      >
        {columns.map((column) => (
          <TableCell
            key={column.id}
            className={cn('truncate px-4 py-0', column.className)}
            onClick={
              column.isActions
                ? (event) => {
                    event.stopPropagation()
                  }
                : undefined
            }
          >
            {column.isActions ? (
              <div
                className={cn(
                  'flex justify-end',
                  column.revealOnHover !== false &&
                    'has-aria-expanded:opacity-100 transition-opacity lg:opacity-0 lg:focus-within:opacity-100 lg:group-hover/row:opacity-100',
                )}
              >
                {column.cell(item)}
              </div>
            ) : (
              column.cell(item)
            )}
          </TableCell>
        ))}
      </TableRow>
      {expanded && (
        <TableRow className="bg-muted/30 hover:bg-muted/30">
          <TableCell colSpan={columns.length} className="whitespace-normal px-4 py-3">
            <div className="sticky left-0 w-[calc(100cqw-2rem)] lg:static lg:w-auto">
              {rowExpanded?.(item)}
            </div>
          </TableCell>
        </TableRow>
      )}
    </>
  )
}

function StateRow({ columnCount, children }: { columnCount: number; children: ReactNode }) {
  return (
    <TableRow className="hover:bg-transparent">
      <TableCell colSpan={columnCount} className="whitespace-normal p-0">
        {children}
      </TableCell>
    </TableRow>
  )
}
