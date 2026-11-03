import type { ConfiguredModelSummary, MachinePoolSummary, ToolCatalog } from '@omnara/sdk'
import { useMemo, useReducer, useState } from 'react'

import {
  type AgentConfigMode,
  agentConfigModeReducer,
  initialAgentConfigModeState,
} from '@/components/agents/agentConfigModeMachine'
import {
  type AgentTemplate,
  agentTemplateBasicConfig,
  agentTemplateName,
  defaultAgentMachineSources,
  defaultAgentTools,
} from '@/components/agents/agentTemplates'
import { takeMcpBuilderOAuthRestore } from '@/components/agents/pendingMcpBuilderOAuth'
import {
  createBasicConfigSession,
  emptyBasicConfig,
  useAgentBuilderForm,
} from '@/components/agents/useAgentBuilderForm'
import { useAgentUnsavedChangesWarning } from '@/components/agents/useAgentUnsavedChangesWarning'

export function useAgentDraft(
  catalog: ToolCatalog | undefined,
  defaultPool: MachinePoolSummary | undefined,
  defaultModel: ConfiguredModelSummary | undefined,
  initialTemplate: AgentTemplate | undefined,
  scope: { orgId: string; projectId: string },
) {
  const [mode, dispatchMode] = useReducer(
    agentConfigModeReducer,
    initialAgentConfigModeState('builder'),
  )
  const [restored] = useState(takeMcpBuilderOAuthRestore)
  const [session, setSession] = useState(() =>
    createBasicConfigSession(restored?.draft.originalSource ?? ''),
  )
  const [initial] = useState(() => {
    const draft = initialTemplate
      ? agentTemplateBasicConfig(initialTemplate, catalog, defaultPool, defaultModel)
      : {
          ...emptyBasicConfig,
          tools: defaultAgentTools(catalog),
          machineSources: defaultAgentMachineSources(defaultPool),
        }
    return { name: initialTemplate?.name ?? '', draft, yaml: session.apply(draft) }
  })
  const [name, setName] = useState(restored?.agentName ?? initial.name)
  const form = useAgentBuilderForm(session, restored?.draft ?? initial.draft, scope)
  const dirty =
    name !== initial.name || (mode.editorYaml ?? form.yaml) !== initial.yaml || form.advancedDirty
  const yamlSession = useMemo(
    () => (mode.editorYaml === null ? null : createBasicConfigSession(mode.editorYaml)),
    [mode.editorYaml],
  )
  const compatibility =
    mode.mode === 'yaml'
      ? (yamlSession?.diagnostics ?? [])
      : [
          ...session.diagnostics.filter((issue) => issue.kind !== 'fatal' && !issue.error),
          ...form.validationIssues,
        ]
  const suppressUnsavedChangesWarning = useAgentUnsavedChangesWarning(dirty)
  const switchMode = (nextMode: AgentConfigMode) => {
    if (nextMode === 'builder' && mode.editorYaml !== null) {
      const adopted = yamlSession
      if (adopted?.initialDraft != null && !adopted.diagnostics.some((issue) => issue.error)) {
        setSession(adopted)
        form.reset(adopted.initialDraft)
        dispatchMode({ type: 'adopt-yaml-edits' })
        return
      }
      return
    }
    dispatchMode({ type: 'switch-mode', mode: nextMode })
  }

  function applyTemplate(template: AgentTemplate) {
    const next = agentTemplateBasicConfig(template, catalog, defaultPool, defaultModel)
    if (form.model.providerConfig !== '' && form.model.modelName !== '') {
      next.providerConfig = form.model.providerConfig
      next.modelName = form.model.modelName
    }
    form.reset(next)
    setName((prev) => agentTemplateName(prev, template))
  }

  return {
    name,
    compatibility,
    setName,
    mode,
    dispatchMode,
    form,
    switchMode,
    applyTemplate,
    suppressUnsavedChangesWarning,
  }
}
