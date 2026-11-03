import { useEffect, useMemo, useReducer, useState } from 'react'

import {
  type AgentConfigMode,
  agentConfigModeReducer,
  initialAgentConfigModeState,
  yamlDiverged,
} from '@/components/agents/agentConfigModeMachine'
import { takeMcpBuilderOAuthRestore } from '@/components/agents/pendingMcpBuilderOAuth'
import {
  createBasicConfigSession,
  useAgentBuilderForm,
} from '@/components/agents/useAgentBuilderForm'
import { useAgentUnsavedChangesWarning } from '@/components/agents/useAgentUnsavedChangesWarning'

export type AgentConfigEditorState = ReturnType<typeof useAgentConfigEditor>

export function useAgentConfigEditor({
  orgId,
  projectId,
  source,
  canManage,
  preferredMode,
  onModeChange,
  onDirtyChange,
}: {
  orgId: string
  projectId: string
  source: string
  canManage: boolean
  preferredMode: AgentConfigMode
  onModeChange: (mode: AgentConfigMode) => void
  onDirtyChange: (dirty: boolean) => void
}) {
  const [restored] = useState(takeMcpBuilderOAuthRestore)
  const [session, setSession] = useState(() =>
    createBasicConfigSession(restored?.draft.originalSource ?? source),
  )
  const builderSession = canManage && session.initialDraft != null ? session : null
  const form = useAgentBuilderForm(session, restored?.draft, { orgId, projectId })
  const [mode, dispatchMode] = useReducer(
    agentConfigModeReducer,
    initialAgentConfigModeState(builderSession ? preferredMode : 'yaml'),
  )

  const builderYaml = builderSession ? form.yaml : source
  const yamlSession = useMemo(
    () => createBasicConfigSession(mode.editorYaml ?? builderYaml),
    [mode.editorYaml, builderYaml],
  )

  const switchMode = (nextMode: AgentConfigMode) => {
    if (nextMode === 'builder' && mode.editorYaml !== null) {
      const adopted = yamlSession
      if (adopted.initialDraft != null && !adopted.diagnostics.some((issue) => issue.error)) {
        setSession(adopted)
        form.reset(adopted.initialDraft)
        dispatchMode({ type: 'adopt-yaml-edits' })
        return
      }
    }
    if (nextMode === 'builder' && yamlSession.initialDraft == null) return
    if (nextMode === 'builder' && yamlSession.diagnostics.some((issue) => issue.error)) return
    dispatchMode({ type: 'switch-mode', mode: nextMode })
  }

  const showBuilder = mode.mode === 'builder' && builderSession != null
  const editorYaml = mode.editorYaml ?? builderYaml
  const yaml = showBuilder ? builderYaml : editorYaml
  const dirty = yaml !== source || (showBuilder && form.advancedDirty)
  const suppressUnsavedChangesWarning = useAgentUnsavedChangesWarning(dirty)
  const saveBlocked =
    yaml.trim() === '' ||
    (!showBuilder && yamlSession.diagnostics.some((issue) => issue.error)) ||
    (builderSession != null && form.blocked && (showBuilder || !yamlDiverged(mode)))

  useEffect(() => {
    onModeChange(mode.mode)
  }, [mode.mode, onModeChange])

  useEffect(() => {
    onDirtyChange(dirty)
  }, [dirty, onDirtyChange])

  return {
    source,
    compatibility: showBuilder
      ? [...session.diagnostics.filter((issue) => !issue.error), ...form.validationIssues]
      : yamlSession.diagnostics,
    canManage,
    builderSession,
    form,
    mode,
    dispatchMode,
    switchMode,
    builderYaml,
    editorYaml,
    yaml,
    dirty,
    suppressUnsavedChangesWarning,
    saveBlocked,
    showBuilder,
  }
}
