import { useNavigate, useSearch } from '@tanstack/react-router'

import { SecretsSection } from '@/components/overview/SecretsSection'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { ProjectSecretGrantsTable } from '@/components/projects/ProjectSecretGrantsTable'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

export function ProjectSecretsPage() {
  const tab = useSearch({
    from: '/authenticated/onboarded/projects/$projectId/secrets',
    select: (search) => search.tab ?? 'project',
  })
  const navigate = useNavigate({ from: '/projects/$projectId/secrets' })

  const sourceTabs = (
    <TabsList aria-label="Secret source">
      <TabsTrigger value="project">Project</TabsTrigger>
      <TabsTrigger value="shared">Shared</TabsTrigger>
    </TabsList>
  )

  return (
    <ProjectPageFrame>
      {({ activeOrg, projectId, project }) => (
        <Tabs
          value={tab}
          onValueChange={(next) => {
            void navigate({ search: next === 'shared' ? { tab: 'shared' } : {} })
          }}
          className="gap-6"
        >
          <TabsContent value="project">
            <SecretsSection
              actions={sourceTabs}
              owner={{ kind: 'project', project_id: projectId }}
              canRead={project?.access.can_manage ?? false}
              canManage={project?.access.can_manage ?? false}
            />
          </TabsContent>
          <TabsContent value="shared">
            {project?.access.can_manage ? (
              <ProjectSecretGrantsTable
                actions={sourceTabs}
                orgId={activeOrg.id}
                projectId={projectId}
                projectName={project.name}
                canManage={project.access.can_manage}
              />
            ) : (
              <div className="flex flex-col gap-3">
                <div className="flex justify-end">{sourceTabs}</div>
                <p className="text-muted-foreground text-sm">
                  You don&rsquo;t have permission to view shared secrets in this project.
                </p>
              </div>
            )}
          </TabsContent>
        </Tabs>
      )}
    </ProjectPageFrame>
  )
}
