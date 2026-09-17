import { type ShouldBlockFn, useBlocker } from '@tanstack/react-router'

let suppressed = false
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
const enableBeforeUnload = () => !suppressed

export function suppressUnsavedChangesWarning() {
  suppressed = true
  const restore = () => {
    suppressed = false
    window.removeEventListener('pageshow', restore)
  }
  window.addEventListener('pageshow', restore)
  return restore
}

export function useUnsavedChangesWarning(dirty: boolean) {
  useBlocker({
    shouldBlockFn: confirmLeavingEditor,
    enableBeforeUnload,
    disabled: !dirty,
  })
}
