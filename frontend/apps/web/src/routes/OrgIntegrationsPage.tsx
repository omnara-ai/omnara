import { useOrgIntegrations } from '@omnara/react'

import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { IntegrationCardList } from '@/components/integrations/IntegrationsList'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { ProjectPickerButton } from '@/components/projects/ProjectPickerButton'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader } from '@/components/ui/empty'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useProjectDirectory } from '@/hooks/use-project-directory'
import { useResourceList } from '@/hooks/use-resource-list'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

/** Every integration across the projects the viewer can read, each tagged with its project. */
export function OrgIntegrationsPage() {
  const { activeOrg } = useActiveOrg()
  const list = useResourceList('-created_at')
  const query = useOrgIntegrations(activeOrg.id, { filters: list.apiFilters })
  const integrations = useInfiniteQueryItems(query)
  const directory = useProjectDirectory(activeOrg.id)

  const addButton = (offerNewProject: boolean) => (
    <ProjectPickerButton
      orgId={activeOrg.id}
      to="/projects/$projectId/integrations/new"
      label="Add integration"
      offerNewProject={offerNewProject}
    />
  )

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-3">
      <SearchHeader
        title="Integrations"
        description="Connect services, choose how agents start, and configure the capabilities they use."
        toolbar={
          <ResourceListToolbar
            search={list.search}
            onSearchChange={list.setSearch}
            placeholder="Search integrations by name…"
            showSearch
          />
        }
      >
        {integrations.length > 0 && addButton(false)}
      </SearchHeader>
      <IntegrationCardList
        orgId={activeOrg.id}
        query={query}
        integrations={integrations}
        projectOf={(integration) => directory.projects.get(integration.project_id)}
        empty={
          <Empty className="rounded-xl border">
            <EmptyHeader>
              <EmptyDescription>
                {list.isFiltering
                  ? 'No integrations match your search.'
                  : 'No integrations yet. Add one to a project to start agents from Slack, GitHub, and more.'}
              </EmptyDescription>
            </EmptyHeader>
            {!list.isFiltering && (
              <EmptyContent>{addButton(canManageOrg(activeOrg.role))}</EmptyContent>
            )}
          </Empty>
        }
      />
    </div>
  )
}
