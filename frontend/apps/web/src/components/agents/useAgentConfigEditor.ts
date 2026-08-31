import { useEffect, useReducer, useState } from 'react'

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

export type AgentConfigEditorState = ReturnType<typeof useAgentConfigEditor>

export function useAgentConfigEditor({
  source,
  canManage,
  preferredMode,
  onModeChange,
  onDirtyChange,
}: {
  source: string
  canManage: boolean
  preferredMode: AgentConfigMode
  onModeChange: (mode: AgentConfigMode) => void
  onDirtyChange: (dirty: boolean) => void
}) {
  const [session, setSession] = useState(() => createBasicConfigSession(source))
  const builderSession = canManage && session.initialDraft != null ? session : null
  const [restored] = useState(takeMcpBuilderOAuthRestore)
  const form = useAgentBuilderForm(session, restored?.draft)
  const [mode, dispatchMode] = useReducer(
    agentConfigModeReducer,
    initialAgentConfigModeState(builderSession ? preferredMode : 'yaml'),
  )

  const switchMode = (nextMode: AgentConfigMode) => {
    if (nextMode === 'builder' && mode.editorYaml !== null) {
      const adopted = createBasicConfigSession(mode.editorYaml)
      if (adopted.initialDraft != null) {
        setSession(adopted)
        form.reset(adopted.initialDraft)
        dispatchMode({ type: 'adopt-yaml-edits' })
        return
      }
    }
    dispatchMode({ type: 'switch-mode', mode: nextMode })
  }

  const showBuilder = mode.mode === 'builder'
  const builderYaml = builderSession ? form.yaml : source
  const editorYaml = mode.editorYaml ?? builderYaml
  const yaml = showBuilder ? builderYaml : editorYaml
  const dirty = yaml !== source
  const saveBlocked =
    yaml.trim() === '' ||
    (builderSession != null && form.blocked && (showBuilder || !yamlDiverged(mode)))

  useEffect(() => {
    onModeChange(mode.mode)
  }, [mode.mode, onModeChange])

  useEffect(() => {
    onDirtyChange(dirty)
  }, [dirty, onDirtyChange])

  return {
    source,
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
    saveBlocked,
    showBuilder,
  }
}
