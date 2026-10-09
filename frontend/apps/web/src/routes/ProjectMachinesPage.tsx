import { ProjectMachinesView } from '@/components/projects/ProjectMachinesView'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'

export function ProjectMachinesPage() {
  return (
    <ProjectPageFrame>
      {({ activeOrg, projectId, project }) =>
        project?.access.can_read ? (
          <ProjectMachinesView
            orgId={activeOrg.id}
            projectId={projectId}
            canManageAccess={project.access.can_manage_access}
          />
        ) : (
          <p className="text-muted-foreground text-sm">
            You don&rsquo;t have permission to view shared machines in this project.
          </p>
        )
      }
    </ProjectPageFrame>
  )
}
