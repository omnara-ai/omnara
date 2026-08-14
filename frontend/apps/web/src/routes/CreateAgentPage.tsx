import { useProjectMachinePoolGrants, useToolCatalog } from '@omnara/react'
import { useSearch } from '@tanstack/react-router'

import { agentTemplates } from '@/components/agents/agentTemplates'
import { CreateAgentForm } from '@/components/agents/CreateAgentForm'
import { Button } from '@/components/ui/button'
import { FullPageSpinner, Spinner } from '@/components/ui/spinner'
import { errorMessage } from '@/lib/submit-status'
import { useProjectPage } from '@/lib/use-project-page'

export function CreateAgentPage() {
  const { activeOrg, project, projectId, isPending: projectIsPending } = useProjectPage()
  const search = useSearch({ strict: false })

  const toolCatalog = useToolCatalog()
  const canManage = project?.access.can_manage === true
  const poolGrantsQuery = useProjectMachinePoolGrants(activeOrg.id, projectId, {
    sort: 'name',
    pageSize: 1,
    enabled: canManage,
  })
  const catalog = toolCatalog.data
  const defaultPool = poolGrantsQuery.data?.pages[0]?.data[0]?.machine_pool
  const templatesError = toolCatalog.isError || poolGrantsQuery.isError
  const templatesReady = !templatesError && !toolCatalog.isPending && !poolGrantsQuery.isPending
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

  if (linkedTemplate && templatesError) {
    const retrying = toolCatalog.isFetching || poolGrantsQuery.isFetching
    return (
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-2">
        <div
          role="alert"
          className="border-destructive/30 bg-destructive/5 flex items-center justify-between gap-3 rounded-md border px-3 py-2"
        >
          <p className="text-destructive text-sm">
            {errorMessage(
              toolCatalog.error ?? poolGrantsQuery.error,
              'Could not load the template defaults.',
            )}
          </p>
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="shrink-0"
            disabled={retrying}
            onClick={() => {
              if (toolCatalog.isError) void toolCatalog.refetch()
              if (poolGrantsQuery.isError) void poolGrantsQuery.refetch()
            }}
          >
            {retrying && <Spinner />}
            Retry
          </Button>
        </div>
      </div>
    )
  }

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
