import { useProjectMachinePoolGrants, useToolCatalog } from '@omnara/react'
import { useSearch } from '@tanstack/react-router'

import { agentTemplates } from '@/components/agents/agentTemplates'
import { CreateAgentFormView } from '@/components/agents/CreateAgentFormView'
import { Button } from '@/components/ui/button'
import { FullPageSpinner, Spinner } from '@/components/ui/spinner'
import { errorMessage } from '@/lib/submit-status'
import { useProjectPage } from '@/lib/use-project-page'

// Only mounts the form once a deep-linked template's prefill data has loaded,
// since the form's state initializers read it on first render.
export function CreateAgentForm() {
  const { activeOrg, projectId } = useProjectPage()
  const search = useSearch({ strict: false })

  const toolCatalog = useToolCatalog()
  const poolGrantsQuery = useProjectMachinePoolGrants(activeOrg.id, projectId, {
    sort: 'name',
    pageSize: 1,
  })
  const catalog = toolCatalog.data
  const defaultPool = poolGrantsQuery.data?.pages[0]?.data[0]?.machine_pool
  const templatesError = toolCatalog.isError || poolGrantsQuery.isError
  const templatesReady = !templatesError && !toolCatalog.isPending && !poolGrantsQuery.isPending
  const linkedTemplate = agentTemplates.find((template) => template.id === search.template)

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
    <CreateAgentFormView
      catalog={catalog}
      defaultPool={defaultPool}
      templatesReady={templatesReady}
      initialTemplate={linkedTemplate}
    />
  )
}
