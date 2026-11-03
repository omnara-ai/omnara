import { type SameEditor, useUnsavedChangesWarning } from '@/hooks/use-unsaved-changes-warning'

const sameAgentEditor: SameEditor = ({ current, next }) => {
  const sameAgent =
    'agentId' in current.params &&
    'agentId' in next.params &&
    current.params.agentId === next.params.agentId &&
    current.params.projectId === next.params.projectId
  const sameProfile =
    'profileId' in current.params &&
    'profileId' in next.params &&
    current.params.profileId === next.params.profileId &&
    current.params.projectId === next.params.projectId
  const samePage =
    current.pathname === next.pathname &&
    ('template' in current.search ? current.search.template : undefined) ===
      ('template' in next.search ? next.search.template : undefined)
  return sameAgent || sameProfile || samePage
}

export function useAgentUnsavedChangesWarning(dirty: boolean) {
  return useUnsavedChangesWarning(dirty, sameAgentEditor)
}
