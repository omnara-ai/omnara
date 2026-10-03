import { ProjectMachineGrantsTables } from '@/components/projects/ProjectMachineGrantsTables'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'

export function ProjectMachinesPage() {
  return (
    <ProjectPageFrame title="Machines">
      {({ activeOrg, projectId, project }) =>
        project?.access.can_read ? (
          <div className="flex flex-col gap-8">
            <ProjectMachineGrantsTables
              orgId={activeOrg.id}
              projectId={projectId}
              canManageAccess={project.access.can_manage_access}
            />
          </div>
        ) : (
          <p className="text-muted-foreground text-sm">
            You don&rsquo;t have permission to view shared machines in this project.
          </p>
        )
      }
    </ProjectPageFrame>
  )
}
