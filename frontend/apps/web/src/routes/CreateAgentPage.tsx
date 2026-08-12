import { useProjectMachinePoolGrants, useToolCatalog } from '@omnara/react'
import { useSearch } from '@tanstack/react-router'

import { agentTemplates } from '@/components/agents/agentTemplates'
import { CreateAgentForm } from '@/components/agents/CreateAgentForm'
import { FullPageSpinner } from '@/components/ui/spinner'
import { useProjectPage } from '@/lib/use-project-page'

export function CreateAgentPage() {
  const { activeOrg, project, projectId, isPending: projectIsPending } = useProjectPage()
  const search = useSearch({ strict: false })

  const toolCatalog = useToolCatalog()
  // Listing pool grants requires manage permission, so wait for the project
  // and only query for users who may create agents here.
  const canManage = project?.access.can_manage === true
  const poolGrantsQuery = useProjectMachinePoolGrants(activeOrg.id, projectId, {
    sort: 'name',
    pageSize: 1,
    enabled: canManage,
  })
  const catalog = toolCatalog.data
  const defaultPool = poolGrantsQuery.data?.pages[0]?.data[0]?.machine_pool
  // Settled is enough: if either fetch failed, templates still apply with
  // whatever prefill data is available instead of blocking forever.
  const templatesReady = !toolCatalog.isPending && !poolGrantsQuery.isPending
  const linkedTemplate = agentTemplates.find((template) => template.id === search.template)

  if (projectIsPending) return <FullPageSpinner />

  if (!project?.access.can_manage) {
    return (
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-2">
        <h1 className="text-xl font-semibold tracking-tight">
          {project ? 'Not allowed' : 'Project not found'}
        </h1>
        <p className="text-muted-foreground text-sm">
          {project
            ? 'You don’t have permission to create agents in this project.'
            : 'This project doesn’t exist or you don’t have access to it.'}
        </p>
      </div>
    )
  }

  // A template deep link waits for the data the prefill needs, so the form
  // can mount with the template already applied as its initial state.
  if (linkedTemplate && !templatesReady) return <FullPageSpinner />

  return (
    <CreateAgentForm
      orgId={activeOrg.id}
      orgName={activeOrg.name}
      projectId={projectId}
      projectName={project.name}
      catalog={catalog}
      defaultPool={defaultPool}
      templatesReady={templatesReady}
      initialTemplate={linkedTemplate}
    />
  )
}
