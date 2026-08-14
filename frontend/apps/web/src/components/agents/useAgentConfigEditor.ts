import { useEffect, useReducer, useState } from 'react'

import { createBasicConfigSession } from '@/components/agents/agentConfigBasicYaml'
import {
  type AgentConfigMode,
  agentConfigModeReducer,
  initialAgentConfigModeState,
  yamlDiverged,
} from '@/components/agents/agentConfigModeMachine'

export type AgentConfigEditorState = ReturnType<typeof useAgentConfigEditor>

/** Shared builder/YAML editing state for the agent and profile config editors. */
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
  const [session] = useState(() => (canManage ? createBasicConfigSession(source) : null))
  const builderSession = session?.initialDraft != null ? session : null
  const [mode, dispatchMode] = useReducer(
    agentConfigModeReducer,
    initialAgentConfigModeState(builderSession ? preferredMode : 'yaml'),
  )
  // Blocked drafts (incomplete or referencing unavailable resources) still
  // update the buffer, so they count as dirty and the YAML tab mirrors them.
  const [builderYaml, setBuilderYaml] = useState(source)
  const [builderBlocked, setBuilderBlocked] = useState(false)
  const handleBuilderYamlChange = (value: string, blocked: boolean) => {
    setBuilderBlocked(blocked)
    setBuilderYaml(value)
  }

  const showBuilder = mode.mode === 'builder'
  const editorYaml = mode.editorYaml ?? builderYaml
  const yaml = showBuilder ? builderYaml : editorYaml
  const dirty = yaml !== source
  // A blocked builder draft also blocks the YAML tab while it still mirrors
  // the builder; hand-edited YAML (diverged) saves on its own merits.
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
    mode,
    dispatchMode,
    builderYaml,
    editorYaml,
    yaml,
    dirty,
    saveBlocked,
    showBuilder,
    handleBuilderYamlChange,
  }
}
