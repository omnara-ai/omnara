import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { ProjectMachineGrantsTables } from '@/components/projects/ProjectMachineGrantsTables'
import { ProjectModelGrantsTable } from '@/components/projects/ProjectModelGrantsTable'
import { ProjectSecretGrantsTable } from '@/components/projects/ProjectSecretGrantsTable'
import { ProjectSkillGrantsTable } from '@/components/projects/ProjectSkillGrantsTable'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useProjectPage } from '@/lib/use-project-page'

export function ProjectGrantsPage() {
  const { activeOrg, projectId, project } = useProjectPage()

  if (!project) {
    return (
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-2">
        <h1 className="text-xl font-semibold tracking-tight">Project not found</h1>
        <p className="text-muted-foreground text-sm">
          This project doesn&rsquo;t exist or you don&rsquo;t have access to it.
        </p>
      </div>
    )
  }

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb
        items={[{ label: activeOrg.name, to: '/' }, { label: project.name }, { label: 'Grants' }]}
      />
      {project.access.can_manage_access ? (
        <Tabs defaultValue="models" className="gap-6">
          <TabsList>
            <TabsTrigger value="models">Models</TabsTrigger>
            <TabsTrigger value="machines">Machines</TabsTrigger>
            <TabsTrigger value="secrets">Secrets</TabsTrigger>
            <TabsTrigger value="skills">Skills</TabsTrigger>
          </TabsList>
          <TabsContent value="models">
            <ProjectModelGrantsTable orgId={activeOrg.id} projectId={projectId} />
          </TabsContent>
          <TabsContent value="machines" className="flex flex-col gap-8">
            <ProjectMachineGrantsTables orgId={activeOrg.id} projectId={projectId} />
          </TabsContent>
          <TabsContent value="secrets">
            <ProjectSecretGrantsTable
              orgId={activeOrg.id}
              projectId={projectId}
              projectName={project.name}
            />
          </TabsContent>
          <TabsContent value="skills">
            <ProjectSkillGrantsTable
              orgId={activeOrg.id}
              projectId={projectId}
              projectName={project.name}
            />
          </TabsContent>
        </Tabs>
      ) : (
        <p className="text-muted-foreground text-sm">
          You don&rsquo;t have permission to manage grants in this project.
        </p>
      )}
    </div>
  )
}
