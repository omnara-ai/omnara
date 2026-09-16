import { useNavigate, useSearch } from '@tanstack/react-router'

import { IntegrationConnectionOutcome } from '@/components/integrations/IntegrationConnectionOutcome'
import { ProjectIntegrationsSection } from '@/components/integrations/ProjectIntegrationsSection'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'

export function ProjectIntegrationsPage() {
  const search = useSearch({ strict: false })
  const navigate = useNavigate()
  return (
    <ProjectPageFrame title="Integrations">
      {({ activeOrg, projectId, project }) => (
        <>
          <ProjectIntegrationsSection
            key={`${activeOrg.id}:${projectId}`}
            orgId={activeOrg.id}
            projectId={projectId}
            canManage={project?.access.can_manage ?? false}
          />
          <IntegrationConnectionOutcome
            key={`${activeOrg.id}:${projectId}:${search.integration_oauth_flow_id ?? search.integration_oauth ?? search.integration_oauth_error ?? ''}`}
            orgId={activeOrg.id}
            projectId={projectId}
            canManage={project?.access.can_manage ?? false}
            search={search}
            onClose={() => {
              void navigate({
                to: '/projects/$projectId/integrations',
                params: { projectId },
                search: {},
                replace: true,
              })
            }}
            onConnected={() => {
              void navigate({
                to: '/projects/$projectId/integrations',
                params: { projectId },
                search: { integration_oauth: 'success' },
                replace: true,
              })
            }}
          />
        </>
      )}
    </ProjectPageFrame>
  )
}
