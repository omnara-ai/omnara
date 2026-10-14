import { type ShouldBlockFn, useBlocker } from '@tanstack/react-router'
import { useCallback, useRef } from 'react'

export type SameEditor = (transition: Parameters<ShouldBlockFn>[0]) => boolean

export function useUnsavedChangesWarning(dirty: boolean, isSameEditor: SameEditor) {
  const suppressed = useRef(false)
  const enableBeforeUnload = useCallback(() => !suppressed.current, [])
  useBlocker({
    shouldBlockFn: (transition) =>
      !isSameEditor(transition) && !window.confirm('You have unsaved changes. Discard them?'),
    enableBeforeUnload,
    disabled: !dirty,
  })
  return function suppressUnsavedChangesWarning() {
    suppressed.current = true
    const restore = () => {
      suppressed.current = false
      window.removeEventListener('pageshow', restore)
    }
    window.addEventListener('pageshow', restore)
  }
}
