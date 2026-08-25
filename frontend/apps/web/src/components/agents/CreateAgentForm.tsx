import { useProjectMachinePoolGrants, useProjectModelGrants, useToolCatalog } from '@omnara/react'
import type { ProjectModelGrantListItem } from '@omnara/sdk'
import { useSearch } from '@tanstack/react-router'

import { agentTemplates } from '@/components/agents/agentTemplates'
import { CreateAgentFormView } from '@/components/agents/CreateAgentFormView'
import { FullPageSpinner } from '@/components/ui/spinner'
import { exactNameGlob } from '@/hooks/use-resource-list'
import { useProjectPage } from '@/lib/use-project-page'

const preferredAgentModel = {
  providerConfig: 'omnara-openrouter',
  modelName: 'openai/gpt-5.6-sol',
} as const

function isRejectedPreferredAgentModel({ grant, model }: ProjectModelGrantListItem) {
  return (
    model.provider_config === preferredAgentModel.providerConfig &&
    model.name === preferredAgentModel.modelName &&
    grant.supports_tools === false
  )
}

export function CreateAgentForm() {
  const { activeOrg, projectId } = useProjectPage()
  const search = useSearch({ strict: false })
  const toolCatalog = useToolCatalog()
  const poolGrantsQuery = useProjectMachinePoolGrants(activeOrg.id, projectId, {
    sort: 'created_at',
    pageSize: 50,
  })
  const fallbackModelGrantsQuery = useProjectModelGrants(activeOrg.id, projectId, {
    sort: 'created_at',
    pageSize: 2,
  })
  const preferredModelGrantsQuery = useProjectModelGrants(activeOrg.id, projectId, {
    filters: { name: exactNameGlob(preferredAgentModel.modelName) },
    sort: 'created_at',
    pageSize: 100,
  })
  const catalog = toolCatalog.data
  const poolGrants = poolGrantsQuery.data?.pages[0]?.data ?? []
  const defaultPool = (
    poolGrants.find((grant) => grant.machine_pool.management_kind === 'cluster') ?? poolGrants[0]
  )?.machine_pool
  const preferredModel = preferredModelGrantsQuery.data?.pages[0]?.data.find(
    (item) =>
      !isRejectedPreferredAgentModel(item) &&
      item.model.provider_config === preferredAgentModel.providerConfig &&
      item.model.name === preferredAgentModel.modelName,
  )?.model
  const fallbackModel = fallbackModelGrantsQuery.data?.pages[0]?.data.find(
    (item) => !isRejectedPreferredAgentModel(item),
  )?.model
  const defaultModel = preferredModel ?? fallbackModel
  const templatesReady =
    !poolGrantsQuery.isPending &&
    !fallbackModelGrantsQuery.isPending &&
    !preferredModelGrantsQuery.isPending
  const linkedTemplate = agentTemplates.find((template) => template.id === search.template)

  if (toolCatalog.isPending) return <FullPageSpinner />
  if (toolCatalog.isError)
    throw new Error('Failed to load tool catalog', { cause: toolCatalog.error })
  if (linkedTemplate && !templatesReady) return <FullPageSpinner />

  return (
    <CreateAgentFormView
      key={linkedTemplate?.id ?? ''}
      catalog={catalog}
      defaultPool={defaultPool}
      defaultModel={defaultModel}
      templatesReady={templatesReady}
      initialTemplate={linkedTemplate}
    />
  )
}
