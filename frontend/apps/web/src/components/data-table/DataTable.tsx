import type { KeyboardEvent, PointerEvent, ReactNode } from 'react'
import { Fragment, useRef, useState } from 'react'

import { type ColumnResizeState, resizeColumnPair } from '@/components/data-table/column-resizing'
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

function measureColumns(target: HTMLElement) {
  const row = target.closest('tr')
  if (!row) return null
  return Array.from(row.children, (cell) => cell.getBoundingClientRect().width)
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
  const [columnWidths, setColumnWidths] = useState<number[] | null>(null)
  const [resizingColumn, setResizingColumn] = useState<number | null>(null)
  const columnResize = useRef<ColumnResizeState | null>(null)
  const columnCount = columns.length

  function beginColumnResize(index: number, event: PointerEvent<HTMLSpanElement>) {
    event.preventDefault()
    event.stopPropagation()
    const widths = measureColumns(event.currentTarget)
    if (!widths) return
    columnResize.current = { index, startX: event.clientX, widths }
    setColumnWidths(widths)
    setResizingColumn(index)
    event.currentTarget.setPointerCapture(event.pointerId)
  }

  function moveColumnResize(event: PointerEvent<HTMLSpanElement>) {
    const resize = columnResize.current
    if (!resize) return
    event.preventDefault()
    setColumnWidths(
      resizeColumnPair(resize.widths, resize.index, event.clientX - resize.startX, columns),
    )
  }

  function endColumnResize(event: PointerEvent<HTMLSpanElement>) {
    if (!columnResize.current) return
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId)
    }
    columnResize.current = null
    setResizingColumn(null)
  }

  function resizeColumnWithKeyboard(index: number, event: KeyboardEvent<HTMLSpanElement>) {
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
    event.preventDefault()
    event.stopPropagation()
    const widths = columnWidths ?? measureColumns(event.currentTarget)
    if (!widths) return
    setColumnWidths(resizeColumnPair(widths, index, event.key === 'ArrowLeft' ? -12 : 12, columns))
  }

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
      <div className="overflow-hidden rounded-xl border">
        <Table className="table-fixed">
          <colgroup>
            {columns.map((column, index) => (
              <col
                key={column.id}
                style={columnWidths ? { width: columnWidths[index] } : undefined}
              />
            ))}
          </colgroup>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              {columns.map((column, index) => (
                <TableHead key={column.id} className={cn('relative px-4', column.className)}>
                  {column.header}
                  {index < columnCount - 1 && (
                    <button
                      type="button"
                      aria-label={`Resize ${column.header || 'actions'} column`}
                      className={cn(
                        'after:bg-border hover:after:bg-ring focus-visible:after:bg-ring absolute inset-y-0 -right-1 z-20 w-2 cursor-col-resize touch-none select-none border-0 bg-transparent p-0 outline-none after:absolute after:inset-y-2 after:left-1/2 after:w-px after:transition-colors',
                        resizingColumn === index && 'after:bg-ring',
                      )}
                      onPointerDown={(event) => {
                        beginColumnResize(index, event)
                      }}
                      onPointerMove={moveColumnResize}
                      onPointerUp={endColumnResize}
                      onPointerCancel={endColumnResize}
                      onKeyDown={(event) => {
                        resizeColumnWithKeyboard(index, event)
                      }}
                      onDoubleClick={() => {
                        setColumnWidths(null)
                      }}
                    />
                  )}
                </TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody>
            {isPending ? (
              [0, 1].map((index) => (
                <TableRow key={index} className="h-12 hover:bg-transparent">
                  <TableCell colSpan={columnCount} className="px-4">
                    <Skeleton className={index === 0 ? 'h-4 w-40' : 'h-4 w-28'} />
                  </TableCell>
                </TableRow>
              ))
            ) : isError ? (
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
            ) : data.length === 0 ? (
              <StateRow columnCount={columnCount}>
                <Empty>
                  <EmptyHeader>
                    <EmptyDescription>{isFiltered ? 'No results.' : emptyMessage}</EmptyDescription>
                  </EmptyHeader>
                </Empty>
              </StateRow>
            ) : (
              data.map((item) => {
                const id = getRowId(item)
                const isExpanded = rowExpanded ? expanded.has(id) : false
                return (
                  <Fragment key={id}>
                    <TableRow
                      className={cn(
                        'group/row h-11',
                        (rowExpanded ?? onRowClick) && 'cursor-pointer',
                        isExpanded && 'border-b-0',
                      )}
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
                                  'has-aria-expanded:opacity-100 opacity-0 transition-opacity focus-within:opacity-100 group-hover/row:opacity-100',
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
                    {isExpanded && (
                      <TableRow className="bg-muted/30 hover:bg-muted/30">
                        <TableCell colSpan={columnCount} className="whitespace-normal px-4 py-3">
                          {rowExpanded?.(item)}
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                )
              })
            )}
          </TableBody>
        </Table>
        {pagination && (pagination.canPrev || pagination.canNext || pagination.page > 0) && (
          <div className="bg-muted/50 flex items-center justify-end gap-3 border-t px-4 py-1.5">
            <span className="text-muted-foreground/70 text-xs tabular-nums">
              Page {pagination.page + 1}
            </span>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="text-muted-foreground hover:text-foreground h-6 px-1.5 text-xs"
              disabled={!pagination.canPrev}
              onClick={pagination.onPrev}
            >
              Previous
            </Button>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="text-muted-foreground hover:text-foreground h-6 px-1.5 text-xs"
              disabled={!pagination.canNext}
              onClick={pagination.onNext}
            >
              Next
            </Button>
          </div>
        )}
      </div>
    </section>
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
