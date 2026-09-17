import { type ShouldBlockFn, useBlocker } from '@tanstack/react-router'
import { useCallback, useRef } from 'react'

const confirmLeavingEditor: ShouldBlockFn = ({ current, next }) => {
  const sameAgent =
    'agentId' in current.params &&
    'agentId' in next.params &&
    current.params.agentId === next.params.agentId &&
    current.params.projectId === next.params.projectId
  const samePage =
    current.pathname === next.pathname &&
    ('template' in current.search ? current.search.template : undefined) ===
      ('template' in next.search ? next.search.template : undefined)
  if (sameAgent || samePage) return false
  return !window.confirm('You have unsaved changes. Discard them?')
}

export function useUnsavedChangesWarning(dirty: boolean) {
  const suppressed = useRef(false)
  const enableBeforeUnload = useCallback(() => !suppressed.current, [])
  useBlocker({
    shouldBlockFn: confirmLeavingEditor,
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
