import { IntegrationsList } from '@/components/integrations/IntegrationsList'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'

export function IntegrationsPage() {
  return (
    <ProjectPageFrame title="Integrations">
      {({ activeOrg, projectId, project }) => (
        <IntegrationsList
          orgId={activeOrg.id}
          projectId={projectId}
          canManage={project?.access.can_manage ?? false}
        />
      )}
    </ProjectPageFrame>
  )
}
