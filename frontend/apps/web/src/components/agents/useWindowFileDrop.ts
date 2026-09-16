import { useEffect, useEffectEvent, useState } from 'react'

import { dragHasFiles } from '@/lib/file-drop-guard'

export function useWindowFileDrop(
  acceptsFiles: boolean,
  onFiles: (files: FileList | null) => void,
): boolean {
  const [dragging, setDragging] = useState(false)
  const dropFiles = useEffectEvent(onFiles)

  useEffect(() => {
    if (!acceptsFiles) return
    function onDragOver(event: DragEvent) {
      if (!dragHasFiles(event)) return
      event.preventDefault()
      if (event.dataTransfer != null) event.dataTransfer.dropEffect = 'copy'
      setDragging(true)
    }

    function onDragLeave(event: DragEvent) {
      if (event.relatedTarget == null) setDragging(false)
    }

    function onDrop(event: DragEvent) {
      if (!dragHasFiles(event)) return
      event.preventDefault()
      setDragging(false)
      dropFiles(event.dataTransfer?.files ?? null)
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
