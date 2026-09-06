import { useEffect, useEffectEvent, useState } from 'react'

function hasFiles(event: DragEvent): boolean {
  return Array.from(event.dataTransfer?.types ?? []).includes('Files')
}

export function useWindowFileDrop(
  acceptsFiles: boolean,
  onFiles: (files: FileList | null) => void,
): boolean {
  const [dragging, setDragging] = useState(false)
  const dropFiles = useEffectEvent(onFiles)

  useEffect(() => {
    function onDragOver(event: DragEvent) {
      if (!hasFiles(event)) return
      event.preventDefault()
      if (event.dataTransfer != null) event.dataTransfer.dropEffect = acceptsFiles ? 'copy' : 'none'
      if (acceptsFiles) setDragging(true)
    }

    function onDragLeave(event: DragEvent) {
      if (event.relatedTarget == null) setDragging(false)
    }

    function onDrop(event: DragEvent) {
      if (!hasFiles(event)) return
      event.preventDefault()
      setDragging(false)
      if (acceptsFiles) dropFiles(event.dataTransfer?.files ?? null)
    }

    window.addEventListener('dragover', onDragOver)
    window.addEventListener('dragleave', onDragLeave)
    window.addEventListener('drop', onDrop)
    return () => {
      window.removeEventListener('dragover', onDragOver)
      window.removeEventListener('dragleave', onDragLeave)
      window.removeEventListener('drop', onDrop)
    }
  }, [acceptsFiles])

  return dragging
}
