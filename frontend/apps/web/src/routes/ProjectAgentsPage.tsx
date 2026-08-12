import { AgentProfilesSection } from '@/components/agents/AgentProfilesSection'
import { AgentsSection } from '@/components/agents/AgentsSection'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'

export function ProjectAgentsPage() {
  return (
    <ProjectPageFrame title="Agents">
      {({ activeOrg, projectId, project }) => (
        <div className="flex flex-col gap-8">
          <AgentProfilesSection
            orgId={activeOrg.id}
            projectId={projectId}
            canOperate={project?.access.can_operate ?? false}
            canManage={project?.access.can_manage ?? false}
          />
          <AgentsSection
            orgId={activeOrg.id}
            projectId={projectId}
            canManage={project?.access.can_manage ?? false}
          />
        </div>
      )}
    </ProjectPageFrame>
  )
}
