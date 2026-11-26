import { type RefObject, useEffect, useEffectEvent, useState } from 'react'

import { dragHasFiles } from '@/lib/file-drop-guard'

export function useFileDrop(
  acceptsFiles: boolean,
  onFiles: (files: FileList | null) => void,
  scope?: RefObject<HTMLElement | null>,
): boolean {
  const [dragging, setDragging] = useState(false)
  const dropFiles = useEffectEvent(onFiles)

  useEffect(() => {
    if (!acceptsFiles) return
    const target = scope == null ? document.documentElement : scope.current
    if (target == null) return
    function onDragOver(event: DragEvent) {
      if (!dragHasFiles(event)) return
      if (scope != null) event.stopPropagation()
      event.preventDefault()
      if (event.dataTransfer != null) event.dataTransfer.dropEffect = 'copy'
      setDragging(true)
    }

    const onDragLeave = (event: DragEvent) => {
      if (!(event.relatedTarget instanceof Node) || !target.contains(event.relatedTarget)) {
        setDragging(false)
      }
    }

    function clearDragging() {
      setDragging(false)
    }

    function onDrop(event: DragEvent) {
      if (!dragHasFiles(event)) return
      if (scope != null) event.stopPropagation()
      event.preventDefault()
      setDragging(false)
      dropFiles(event.dataTransfer?.files ?? null)
    }

    target.addEventListener('dragover', onDragOver)
    target.addEventListener('dragleave', onDragLeave)
    target.addEventListener('drop', onDrop)
    window.addEventListener('drop', clearDragging, true)
    window.addEventListener('dragend', clearDragging)
    return () => {
      target.removeEventListener('dragover', onDragOver)
      target.removeEventListener('dragleave', onDragLeave)
      target.removeEventListener('drop', onDrop)
      window.removeEventListener('drop', clearDragging, true)
      window.removeEventListener('dragend', clearDragging)
    }
  }, [acceptsFiles, scope])

  return dragging
}
