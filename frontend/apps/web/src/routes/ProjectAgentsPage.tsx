import { AgentsSection } from '@/components/agents/AgentsSection'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'

export function ProjectAgentsPage() {
  return (
    <ProjectPageFrame title="Agents">
      {({ activeOrg, projectId, project }) => (
        <AgentsSection
          orgId={activeOrg.id}
          projectId={projectId}
          canOperate={project?.access.can_operate ?? false}
          canManage={project?.access.can_manage ?? false}
        />
      )}
    </ProjectPageFrame>
  )
}
