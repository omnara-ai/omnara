import { useProjectMachinePoolGrants, useProjectModelGrants, useToolCatalog } from '@omnara/react'
import type { ConfiguredModelSummary, MachinePoolSummary } from '@omnara/sdk'

import { type AgentTemplate, agentTemplateBasicConfig } from '@/components/agents/agentTemplates'
import type { BasicConfig } from '@/components/agents/useAgentBuilderForm'

interface TemplateDefaults {
  ready: boolean
  build: (template: AgentTemplate) => BasicConfig
  defaultModel?: ConfiguredModelSummary
  defaultPool?: MachinePoolSummary
}

export function useTemplateDefaults(orgId: string, projectId: string): TemplateDefaults {
  const toolCatalog = useToolCatalog()
  const poolGrantsQuery = useProjectMachinePoolGrants(orgId, projectId, {
    sort: 'created_at',
    pageSize: 50,
  })
  const modelGrantsQuery = useProjectModelGrants(orgId, projectId, {
    sort: 'created_at',
    pageSize: 1,
  })
  const poolGrants = poolGrantsQuery.data?.pages[0]?.data ?? []
  const defaultPool = (
    poolGrants.find((grant) => grant.machine_pool.management_kind === 'cluster') ?? poolGrants[0]
  )?.machine_pool
  const defaultModel = modelGrantsQuery.data?.pages[0]?.data[0]?.model
  return {
    ready: !toolCatalog.isPending && !poolGrantsQuery.isPending && !modelGrantsQuery.isPending,
    defaultModel,
    defaultPool,
    build: (template) =>
      agentTemplateBasicConfig(template, toolCatalog.data, defaultPool, defaultModel),
  }
}
