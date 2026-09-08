import type { ConfiguredModelSummary, MachinePoolSummary, ToolCatalog } from '@omnara/sdk'
import { useReducer, useState } from 'react'

import {
  type AgentConfigMode,
  agentConfigModeReducer,
  initialAgentConfigModeState,
} from '@/components/agents/agentConfigModeMachine'
import {
  type AgentTemplate,
  agentTemplateBasicConfig,
  agentTemplateName,
  defaultAgentTools,
} from '@/components/agents/agentTemplates'
import { takeMcpBuilderOAuthRestore } from '@/components/agents/pendingMcpBuilderOAuth'
import {
  createBasicConfigSession,
  emptyBasicConfig,
  useAgentBuilderForm,
} from '@/components/agents/useAgentBuilderForm'

export function useAgentDraft(
  catalog: ToolCatalog | undefined,
  defaultPool: MachinePoolSummary | undefined,
  defaultModel: ConfiguredModelSummary | undefined,
  initialTemplate: AgentTemplate | undefined,
) {
  const [mode, dispatchMode] = useReducer(
    agentConfigModeReducer,
    initialAgentConfigModeState('builder'),
  )
  const [restored] = useState(takeMcpBuilderOAuthRestore)
  const [name, setName] = useState(restored?.agentName ?? initialTemplate?.name ?? '')
  const [session, setSession] = useState(() => createBasicConfigSession(''))
  const form = useAgentBuilderForm(
    session,
    restored?.draft ??
      (initialTemplate
        ? agentTemplateBasicConfig(initialTemplate, catalog, defaultPool, defaultModel)
        : { ...emptyBasicConfig, tools: defaultAgentTools(catalog) }),
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

  function applyTemplate(template: AgentTemplate) {
    const next = agentTemplateBasicConfig(template, catalog, defaultPool, defaultModel)
    // Keep a model the user already picked; templates only fill the gap.
    if (form.model.providerConfig !== '' && form.model.modelName !== '') {
      next.providerConfig = form.model.providerConfig
      next.modelName = form.model.modelName
    }
    form.reset(next)
    setName((prev) => agentTemplateName(prev, template))
  }

  return { name, setName, mode, dispatchMode, form, switchMode, applyTemplate }
}
