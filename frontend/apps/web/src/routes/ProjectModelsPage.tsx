import { ProjectModelGrantsTable } from '@/components/projects/ProjectModelGrantsTable'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'

export function ProjectModelsPage() {
  return (
    <ProjectPageFrame>
      {({ activeOrg, projectId, project }) =>
        project?.access.can_read ? (
          <ProjectModelGrantsTable
            orgId={activeOrg.id}
            projectId={projectId}
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
