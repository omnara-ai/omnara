import { useProjectMachinePoolGrants, useProjectModelGrants, useToolCatalog } from '@omnara/react'
import { useSearch } from '@tanstack/react-router'

import { agentTemplates } from '@/components/agents/agentTemplates'
import { CreateAgentFormView } from '@/components/agents/CreateAgentFormView'
import { FullPageSpinner } from '@/components/ui/spinner'
import { useProjectPage } from '@/lib/use-project-page'

export function CreateAgentForm() {
  const { activeOrg, projectId } = useProjectPage()
  const search = useSearch({ strict: false })

  const toolCatalog = useToolCatalog()
  const poolGrantsQuery = useProjectMachinePoolGrants(activeOrg.id, projectId, {
    sort: 'created_at',
    pageSize: 50,
  })
  const modelGrantsQuery = useProjectModelGrants(activeOrg.id, projectId, {
    sort: 'created_at',
    pageSize: 1,
  })
  const catalog = toolCatalog.data
  const poolGrants = poolGrantsQuery.data?.pages[0]?.data ?? []
  const defaultPool = (
    poolGrants.find((grant) => grant.machine_pool.management_kind === 'cluster') ?? poolGrants[0]
  )?.machine_pool
  const defaultModel = modelGrantsQuery.data?.pages[0]?.data[0]?.model
  const templatesReady =
    !toolCatalog.isPending && !poolGrantsQuery.isPending && !modelGrantsQuery.isPending
  const linkedTemplate = agentTemplates.find((template) => template.id === search.template)

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
