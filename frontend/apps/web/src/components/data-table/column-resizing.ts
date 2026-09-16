import type { KeyboardEvent, PointerEvent } from 'react'
import { useRef, useState } from 'react'

interface ColumnResizeState {
  index: number
  startX: number
  widths: number[]
}

interface ResizableColumn {
  isActions?: boolean
}

function measureColumns(target: HTMLElement) {
  const row = target.closest('tr')
  if (!row) return null
  return Array.from(row.children, (cell) => cell.getBoundingClientRect().width)
}

export function useColumnResizing(columns: readonly ResizableColumn[]) {
  const [columnWidths, setColumnWidths] = useState<number[] | null>(null)
  const [resizingColumn, setResizingColumn] = useState<number | null>(null)
  const columnResize = useRef<ColumnResizeState | null>(null)

  function begin(index: number, event: PointerEvent<HTMLElement>) {
    event.preventDefault()
    event.stopPropagation()
    const widths = measureColumns(event.currentTarget)
    if (!widths) return
    columnResize.current = { index, startX: event.clientX, widths }
    setColumnWidths(widths)
    setResizingColumn(index)
    event.currentTarget.setPointerCapture(event.pointerId)
  }

  function move(event: PointerEvent<HTMLElement>) {
    const resize = columnResize.current
    if (!resize) return
    event.preventDefault()
    setColumnWidths(
      resizeColumnPair(resize.widths, resize.index, event.clientX - resize.startX, columns),
    )
  }

  function end(event: PointerEvent<HTMLElement>) {
    if (!columnResize.current) return
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId)
    }
    columnResize.current = null
    setResizingColumn(null)
  }

  function resizeWithKeyboard(index: number, event: KeyboardEvent<HTMLElement>) {
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
    event.preventDefault()
    event.stopPropagation()
    const widths = columnWidths ?? measureColumns(event.currentTarget)
    if (!widths) return
    setColumnWidths(resizeColumnPair(widths, index, event.key === 'ArrowLeft' ? -12 : 12, columns))
  }

  function reset() {
    setColumnWidths(null)
  }

  return { columnWidths, resizingColumn, begin, move, end, resizeWithKeyboard, reset }
}

export function resizeColumnPair(
  widths: number[],
  index: number,
  requestedDelta: number,
  columns: readonly ResizableColumn[],
) {
  const nextIndex = index + 1
  const currentWidth = widths[index]
  const nextWidth = widths[nextIndex]
  if (currentWidth === undefined || nextWidth === undefined) return widths

  const currentMinimum = minimumColumnWidth(columns[index])
  const nextMinimum = minimumColumnWidth(columns[nextIndex])
  const delta = Math.max(
    currentMinimum - currentWidth,
    Math.min(requestedDelta, nextWidth - nextMinimum),
  )
  const resized = [...widths]
  resized[index] = currentWidth + delta
  resized[nextIndex] = nextWidth - delta
  return resized
}

function minimumColumnWidth(column: ResizableColumn | undefined) {
  return column?.isActions ? 56 : 96
}
