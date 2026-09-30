import { useNavigate, useSearch } from '@tanstack/react-router'

import { SkillsSection } from '@/components/overview/SkillsSection'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { ProjectSkillGrantsTable } from '@/components/projects/ProjectSkillGrantsTable'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

export function ProjectSkillsPage() {
  const tab = useSearch({
    from: '/authenticated/onboarded/projects/$projectId/skills',
    select: (search) => search.tab ?? 'project',
  })
  const navigate = useNavigate({ from: '/projects/$projectId/skills' })

  return (
    <ProjectPageFrame title="Skills">
      {({ activeOrg, projectId, project }) => (
        <Tabs
          value={tab}
          onValueChange={(next) => {
            void navigate({ search: next === 'shared' ? { tab: 'shared' } : {} })
          }}
          className="gap-6"
        >
          <TabsList aria-label="Skill source">
            <TabsTrigger value="project">Project</TabsTrigger>
            <TabsTrigger value="shared">Shared</TabsTrigger>
          </TabsList>
          <TabsContent value="project">
            <SkillsSection
              owner={{ kind: 'project', project_id: projectId }}
              canRead={project?.access.can_read ?? false}
              canManage={project?.access.can_manage ?? false}
            />
          </TabsContent>
          <TabsContent value="shared">
            {project?.access.can_manage_access ? (
              <ProjectSkillGrantsTable
                orgId={activeOrg.id}
                projectId={projectId}
                projectName={project.name}
              />
            ) : (
              <p className="text-muted-foreground text-sm">
                You don&rsquo;t have permission to view shared skills in this project.
              </p>
            )}
          </TabsContent>
        </Tabs>
      )}
    </ProjectPageFrame>
  )
}
