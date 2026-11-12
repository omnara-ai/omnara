import type { ConfiguredModelSummary, MachinePoolSummary, ToolCatalog } from '@omnara/sdk'
import { useState } from 'react'

import {
  type AgentTemplate,
  agentTemplateBasicConfig,
  agentTemplateName,
  defaultAgentMachineSources,
  defaultAgentTools,
} from '@/components/agents/agentTemplates'
import { useMcpBuilderOAuthRestore } from '@/components/agents/pendingMcpBuilderOAuth'
import {
  createBasicConfigSession,
  emptyBasicConfig,
  useAgentBuilderForm,
} from '@/components/agents/useAgentBuilderForm'
import { useAgentConfigMode } from '@/components/agents/useAgentConfigMode'
import { useAgentUnsavedChangesWarning } from '@/components/agents/useAgentUnsavedChangesWarning'

export function useAgentDraft(
  catalog: ToolCatalog | undefined,
  defaultPool: MachinePoolSummary | undefined,
  defaultModel: ConfiguredModelSummary | undefined,
  initialTemplate: AgentTemplate | undefined,
  scope: { orgId: string; projectId: string },
) {
  const restored = useMcpBuilderOAuthRestore()
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
    return {
      name: initialTemplate?.name ?? '',
      draft,
      yaml: createBasicConfigSession('').apply(draft),
    }
  })
  const [name, setName] = useState(restored?.agentName ?? initial.name)
  const form = useAgentBuilderForm(session, restored?.draft ?? initial.draft, scope)
  const controller = useAgentConfigMode({
    session,
    setSession,
    form,
    source: '',
    builderAvailable: true,
    initialMode: 'builder',
  })
  const dirty = name !== initial.name || controller.yaml !== initial.yaml || form.dirty
  const suppressUnsavedChangesWarning = useAgentUnsavedChangesWarning(dirty)

  function applyTemplate(template: AgentTemplate) {
    const next = agentTemplateBasicConfig(template, catalog, defaultPool, defaultModel)
    if (form.model.providerConfig !== '' && form.model.modelName !== '') {
      next.providerConfig = form.model.providerConfig
      next.modelName = form.model.modelName
    }
    setSession(createBasicConfigSession(''))
    form.reset({ ...next, originalSource: '', advancedEntries: [] })
    controller.dispatchMode({ type: 'adopt-yaml-edits' })
    setName((prev) => agentTemplateName(prev, template))
  }

  return {
    name,
    ...controller,
    setName,
    form,
    applyTemplate,
    suppressUnsavedChangesWarning,
  }
}
