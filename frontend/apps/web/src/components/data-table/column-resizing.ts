export interface ColumnResizeState {
  index: number
  startX: number
  widths: number[]
}

interface ResizableColumn {
  isActions?: boolean
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
