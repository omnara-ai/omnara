import { AgentProfilesSection } from '@/components/agents/AgentProfilesSection'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'

export function ProjectAgentProfilesPage() {
  return (
    <ProjectPageFrame title="Agent profiles">
      {({ activeOrg, projectId, project }) => (
        <AgentProfilesSection
          orgId={activeOrg.id}
          projectId={projectId}
          canManage={project?.access.can_manage ?? false}
        />
      )}
    </ProjectPageFrame>
  )
}
