import { useProjectMachinePoolGrants, useToolCatalog } from '@omnara/react'
import { type Dispatch, type SetStateAction, useCallback } from 'react'

import {
  type BasicConfig,
  newMachineSource,
} from '@/components/agents/agentConfigBasicSerialization'
import {
  type AgentTemplate,
  agentTemplateTools,
  isAgentTemplateName,
} from '@/components/agents/agentTemplates'

/**
 * Fill the builder draft from a template: instruction, tools with their
 * catalog-default permissions, and the project's first granted machine pool
 * when the template calls for one. A user-typed name is preserved; an empty
 * or template-suggested name is replaced.
 */
export function useApplyAgentTemplate({
  orgId,
  projectId,
  setConfigDraft,
  setName,
}: {
  orgId: string
  projectId: string
  setConfigDraft: Dispatch<SetStateAction<BasicConfig>>
  setName: (update: (name: string) => string) => void
}) {
  const toolCatalog = useToolCatalog()
  const poolGrantsQuery = useProjectMachinePoolGrants(orgId, projectId, {
    sort: 'name',
    pageSize: 1,
  })
  const catalog = toolCatalog.data
  const defaultPool = poolGrantsQuery.data?.pages[0]?.data[0]?.machine_pool
  const ready = catalog != null && !poolGrantsQuery.isPending

  const apply = useCallback(
    (template: AgentTemplate) => {
      if (catalog == null) return
      setConfigDraft((prev) => ({
        ...prev,
        instruction: template.instruction,
        tools: agentTemplateTools(template, catalog),
        machineSources:
          template.usesMachinePool && defaultPool
            ? [
                {
                  ...newMachineSource('pool'),
                  name: defaultPool.name,
                  provider: defaultPool.provider,
                  managementKind: defaultPool.management_kind,
                },
              ]
            : [],
      }))
      setName((name) => {
        const trimmed = name.trim()
        return trimmed === '' || isAgentTemplateName(trimmed) ? template.name : name
      })
    },
    [catalog, defaultPool, setConfigDraft, setName],
  )

  return { apply, ready }
}
