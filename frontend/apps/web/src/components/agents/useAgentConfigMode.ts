import { useMemo, useReducer } from 'react'

import {
  type AgentConfigMode,
  agentConfigModeReducer,
  initialAgentConfigModeState,
  yamlDiverged,
} from '@/components/agents/agentConfigModeMachine'
import {
  type BasicConfigSession,
  createBasicConfigSession,
} from '@/components/agents/agentConfigSession'
import type { AgentBuilderForm } from '@/components/agents/useAgentBuilderForm'

export function useAgentConfigMode({
  session,
  setSession,
  form,
  initialMode,
  builderAvailable,
  source,
}: {
  session: BasicConfigSession
  setSession: (session: BasicConfigSession) => void
  form: AgentBuilderForm
  initialMode: AgentConfigMode
  builderAvailable: boolean
  source: string
}) {
  const [mode, dispatchMode] = useReducer(
    agentConfigModeReducer,
    initialAgentConfigModeState(initialMode),
  )
  const builderYaml = builderAvailable ? form.yaml : source
  const yamlSession = useMemo(
    () => (mode.editorYaml === null ? null : createBasicConfigSession(mode.editorYaml)),
    [mode.editorYaml],
  )
  const showBuilder = mode.mode === 'builder' && builderAvailable
  const editorYaml = mode.editorYaml ?? builderYaml
  const compatibility =
    yamlSession?.diagnostics ??
    (builderAvailable
      ? [...session.diagnostics.filter((issue) => !issue.error), ...form.validationIssues]
      : session.diagnostics)
  const saveBlocked =
    editorYaml.trim() === '' ||
    compatibility.some((issue) => issue.error) ||
    (builderAvailable && form.blocked && (showBuilder || !yamlDiverged(mode)))

  function switchMode(nextMode: AgentConfigMode) {
    if (nextMode === 'builder') {
      if (yamlSession) {
        if (!yamlSession.initialDraft) {
          if (builderAvailable) dispatchMode({ type: 'switch-mode', mode: 'builder' })
          return
        }
        setSession(yamlSession)
        form.reset(yamlSession.initialDraft)
        dispatchMode({ type: 'adopt-yaml-edits' })
        return
      }
      if (!builderAvailable) return
    }
    dispatchMode({ type: 'switch-mode', mode: nextMode })
  }

  return {
    mode,
    dispatchMode,
    switchMode,
    showBuilder,
    editorYaml,
    builderYaml,
    yaml: editorYaml,
    compatibility,
    saveBlocked,
  }
}
