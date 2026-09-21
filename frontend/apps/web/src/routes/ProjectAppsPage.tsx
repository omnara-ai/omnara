import { ProjectAppsList } from '@/components/apps/ProjectAppsList'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'

export function ProjectAppsPage() {
  return (
    <ProjectPageFrame title="Apps">
      {({ activeOrg, projectId, project }) => (
        <ProjectAppsList
          orgId={activeOrg.id}
          projectId={projectId}
          canManage={project?.access.can_manage ?? false}
        />
      )}
    </ProjectPageFrame>
  )
}
