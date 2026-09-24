export function dragHasFiles(event: DragEvent): boolean {
  return Array.from(event.dataTransfer?.types ?? []).includes('Files')
}

export function installFileDropGuard(target: Window): () => void {
  function onDragOver(event: DragEvent) {
    if (!dragHasFiles(event)) return
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
