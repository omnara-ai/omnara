import { useEffect, useState } from 'react'

import type { AgentConfigMode } from '@/components/agents/agentConfigModeMachine'
import type { ConfigSourceFormat } from '@/components/agents/agentConfigSourceFormat'
import { takeMcpBuilderOAuthRestore } from '@/components/agents/pendingMcpBuilderOAuth'
import {
  createBasicConfigSession,
  emptyBasicConfig,
  useAgentBuilderForm,
} from '@/components/agents/useAgentBuilderForm'
import { useAgentConfigMode } from '@/components/agents/useAgentConfigMode'
import { useAgentUnsavedChangesWarning } from '@/components/agents/useAgentUnsavedChangesWarning'

export type AgentConfigEditorState = ReturnType<typeof useAgentConfigEditor>

export function useAgentConfigEditor({
  orgId,
  projectId,
  source,
  sourceFormat,
  canManage,
  preferredMode,
  onModeChange,
  onDirtyChange,
}: {
  orgId: string
  projectId: string
  source: string
  sourceFormat?: ConfigSourceFormat
  canManage: boolean
  preferredMode: AgentConfigMode
  onModeChange: (mode: AgentConfigMode) => void
  onDirtyChange: (dirty: boolean) => void
}) {
  const [restored] = useState(takeMcpBuilderOAuthRestore)
  const [session, setSession] = useState(() =>
    createBasicConfigSession(restored?.draft.originalSource ?? source, sourceFormat),
  )
  const builderSession = canManage && session.initialDraft != null ? session : null
  const restoredSource = restored?.draft.savedSource ?? restored?.draft.originalSource
  const restoreConflict = restoredSource !== undefined && restoredSource !== source
  const form = useAgentBuilderForm(
    session,
    {
      ...(restored?.draft ?? session.initialDraft ?? emptyBasicConfig),
      savedSource: restored?.draft.savedSource ?? source,
    },
    { orgId, projectId },
  )
  const controller = useAgentConfigMode({
    session,
    setSession,
    form,
    source,
    builderAvailable: builderSession != null,
    initialMode: builderSession ? preferredMode : 'yaml',
  })
  const { mode, yaml } = controller
  const dirty = yaml !== source || form.dirty
  const suppressUnsavedChangesWarning = useAgentUnsavedChangesWarning(dirty)

  useEffect(() => {
    onModeChange(mode.mode)
  }, [mode.mode, onModeChange])

  useEffect(() => {
    onDirtyChange(dirty)
  }, [dirty, onDirtyChange])

  return {
    source,
    ...controller,
    saveBlocked: controller.saveBlocked || restoreConflict,
    compatibility: restoreConflict
      ? [
          ...controller.compatibility,
          {
            path: [],
            kind: 'fatal' as const,
            error: true,
            message:
              'The configuration changed during OAuth authorization. This restored draft cannot be saved. Review your pending changes, reload the latest configuration, and reapply only your intended edits.',
          },
        ]
      : controller.compatibility,
    canManage,
    builderSession,
    form,
    dirty,
    suppressUnsavedChangesWarning,
  }
}
