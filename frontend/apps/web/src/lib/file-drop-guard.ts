export function dragHasFiles(event: Pick<DragEvent, 'dataTransfer'>): boolean {
  return Array.from(event.dataTransfer?.types ?? []).includes('Files')
}

/** Keep a floating surface's file drops from reaching a background composer. */
export function containFileDrop(
  event: Pick<DragEvent, 'dataTransfer' | 'preventDefault' | 'stopPropagation'>,
) {
  if (!dragHasFiles(event)) return
  event.preventDefault()
  event.stopPropagation()
  if (event.dataTransfer != null) event.dataTransfer.dropEffect = 'none'
}

export function installFileDropGuard(target: Window): () => void {
  function onDragOver(event: DragEvent) {
    if (!dragHasFiles(event) || event.defaultPrevented) return
    event.preventDefault()
    if (event.dataTransfer != null) event.dataTransfer.dropEffect = 'none'
  }

  function onDrop(event: DragEvent) {
    if (dragHasFiles(event)) event.preventDefault()
  }

  target.addEventListener('dragover', onDragOver)
  target.addEventListener('drop', onDrop)
  return () => {
    target.removeEventListener('dragover', onDragOver)
    target.removeEventListener('drop', onDrop)
  }
}
