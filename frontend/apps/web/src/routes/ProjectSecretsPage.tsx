import { SecretsSection } from '@/components/overview/SecretsSection'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'

export function ProjectSecretsPage() {
  return (
    <ProjectPageFrame title="Project Secrets">
      {({ projectId, project }) => (
        <SecretsSection
          owner={{ kind: 'project', project_id: projectId }}
          canRead={project?.access.can_manage ?? false}
          canManage={project?.access.can_manage ?? false}
        />
      )}
    </ProjectPageFrame>
  )
}
