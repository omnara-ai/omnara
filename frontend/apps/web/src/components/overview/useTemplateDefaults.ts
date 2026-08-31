import { useToolCatalog } from '@omnara/react'
import type { ConfiguredModelSummary, MachinePoolSummary } from '@omnara/sdk'

import { type AgentTemplate, agentTemplateBasicConfig } from '@/components/agents/agentTemplates'
import type { BasicConfig } from '@/components/agents/useAgentBuilderForm'
import { useProjectDefaults } from '@/components/agents/useProjectDefaults'

interface TemplateDefaults {
  ready: boolean
  build: (template: AgentTemplate) => BasicConfig
  defaultModel?: ConfiguredModelSummary
  defaultPool?: MachinePoolSummary
}

export function useTemplateDefaults(orgId: string, projectId: string): TemplateDefaults {
  const toolCatalog = useToolCatalog()
  const { ready, defaultModel, defaultPool } = useProjectDefaults(orgId, projectId)
  return {
    ready: !toolCatalog.isPending && ready,
    defaultModel,
    defaultPool,
    build: (template) =>
      agentTemplateBasicConfig(template, toolCatalog.data, defaultPool, defaultModel),
  }
}
