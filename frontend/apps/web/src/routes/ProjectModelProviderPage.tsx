import { useModelProviders } from '@omnara/react'
import { useParams } from '@tanstack/react-router'

import { ProjectModelProviderView } from '@/components/projects/ProjectModelProviderView'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { useAllPages } from '@/hooks/use-all-pages'
import { useActiveOrg } from '@/lib/use-active-org'

export function ProjectModelProviderPage() {
  const { activeOrg } = useActiveOrg()
  const { projectId = '', providerId = '' } = useParams({ strict: false })
  const providers = useAllPages(useModelProviders(activeOrg.id))
  const provider = providers.items.find((candidate) => candidate.id === providerId)

  return (
    <ProjectPageFrame
      crumbs={[
        { id: 'models', label: 'Models', to: '/projects/$projectId/models', params: { projectId } },
        { id: 'provider', label: provider?.name ?? 'Provider' },
      ]}
    >
      {({ project }) =>
        project?.access.can_read ? (
          <ProjectModelProviderView
            orgId={activeOrg.id}
            projectId={projectId}
            providerId={providerId}
            canManageAccess={project.access.can_manage_access}
          />
        ) : (
          <p className="text-muted-foreground text-sm">
            You don&rsquo;t have permission to view shared models in this project.
          </p>
        )
      }
    </ProjectPageFrame>
  )
}
