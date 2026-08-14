import { useEffect, useReducer, useState } from 'react'

import {
  type AgentConfigMode,
  agentConfigModeReducer,
  initialAgentConfigModeState,
  yamlDiverged,
} from '@/components/agents/agentConfigModeMachine'
import { createBasicConfigSession } from '@/components/agents/useAgentBuilderForm'

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
  const [session, setSession] = useState(() =>
    canManage ? createBasicConfigSession(source) : null,
  )
  const builderSession = session?.initialDraft != null ? session : null
  const [formGeneration, setFormGeneration] = useState(0)
  const [mode, dispatchMode] = useReducer(
    agentConfigModeReducer,
    initialAgentConfigModeState(builderSession ? preferredMode : 'yaml'),
  )
  const [builderYaml, setBuilderYaml] = useState(source)
  const [builderBlocked, setBuilderBlocked] = useState(false)
  const handleBuilderYamlChange = (value: string, blocked: boolean) => {
    setBuilderBlocked(blocked)
    setBuilderYaml(value)
  }

  const switchMode = (nextMode: AgentConfigMode) => {
    if (nextMode === 'builder' && mode.editorYaml !== null) {
      const adopted = createBasicConfigSession(mode.editorYaml)
      if (adopted.initialDraft != null) {
        setSession(adopted)
        setFormGeneration((generation) => generation + 1)
        setBuilderYaml(mode.editorYaml)
        dispatchMode({
          type: 'editor-yaml-changed',
          yaml: mode.editorYaml,
          builderYaml: mode.editorYaml,
        })
      }
    }
    dispatchMode({ type: 'switch-mode', mode: nextMode })
  }

  const showBuilder = mode.mode === 'builder'
  const editorYaml = mode.editorYaml ?? builderYaml
  const yaml = showBuilder ? builderYaml : editorYaml
  const dirty = yaml !== source
  const saveBlocked = yaml.trim() === '' || (builderBlocked && (showBuilder || !yamlDiverged(mode)))

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
    formGeneration,
    mode,
    dispatchMode,
    switchMode,
    builderYaml,
    editorYaml,
    yaml,
    dirty,
    saveBlocked,
    showBuilder,
    handleBuilderYamlChange,
  }
}
