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
    () =>
      mode.editorYaml === null
        ? null
        : createBasicConfigSession(mode.editorYaml, session.sourceFormat),
    [mode.editorYaml, session.sourceFormat],
  )
  const showBuilder = mode.mode === 'builder' && builderAvailable
  const editorYaml = mode.editorYaml ?? builderYaml
  const compatibility = [
    ...(yamlSession?.diagnostics ??
      (builderAvailable
        ? [...session.diagnostics.filter((issue) => !issue.error), ...form.validationIssues]
        : session.diagnostics)),
  ]
  if (form.unappliedChanges)
    compatibility.push({
      path: [],
      kind: 'advanced',
      error: true,
      message:
        'Some Builder edits cannot be applied yet. Fix or discard those edits before switching to the source editor.',
    })
  const saveBlocked =
    editorYaml.trim() === '' ||
    form.unappliedChanges ||
    compatibility.some((issue) => issue.syntaxError) ||
    (showBuilder
      ? form.blocked
      : !yamlDiverged(mode) && form.dirty && form.validationIssues.length > 0)

  function switchMode(nextMode: AgentConfigMode) {
    if (form.unappliedChanges) return
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
    editorFormat: session.sourceFormat,
    sourceFormat: yamlSession?.sourceFormat ?? session.sourceFormat,
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
