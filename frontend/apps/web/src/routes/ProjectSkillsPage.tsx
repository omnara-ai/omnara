import { SkillsSection } from '@/components/overview/SkillsSection'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'

export function ProjectSkillsPage() {
  return (
    <ProjectPageFrame title="Project Skills">
      {({ projectId, project }) => (
        <SkillsSection
          owner={{ kind: 'project', project_id: projectId }}
          canRead={project?.access.can_read ?? false}
          canManage={project?.access.can_manage ?? false}
        />
      )}
    </ProjectPageFrame>
  )
}
