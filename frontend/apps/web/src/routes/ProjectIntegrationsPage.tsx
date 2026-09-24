import { ProjectIntegrationsList } from '@/components/integrations/ProjectIntegrationsList'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'

export function ProjectIntegrationsPage() {
  return (
    <ProjectPageFrame title="Integrations">
      {({ activeOrg, projectId, project }) => (
        <ProjectIntegrationsList
          orgId={activeOrg.id}
          projectId={projectId}
          canManage={project?.access.can_manage ?? false}
        />
      )}
    </ProjectPageFrame>
  )
}
