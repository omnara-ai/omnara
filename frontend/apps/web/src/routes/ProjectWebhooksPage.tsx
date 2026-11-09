import { EventWebhooksSection } from '@/components/projects/EventWebhooksSection'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'

export default function ProjectWebhooksPage() {
  return (
    <ProjectPageFrame>
      {({ activeOrg, projectId, project }) => (
        <section className="flex flex-col gap-4">
          <div>
            <h1 className="type-title">Webhooks</h1>
            <p className="text-muted-foreground text-sm">
              Receive events from all agents in this project, one profile, or one agent.
            </p>
          </div>
          <EventWebhooksSection
            key={projectId}
            orgId={activeOrg.id}
            projectId={projectId}
            canManage={project?.access.can_manage ?? false}
          />
        </section>
      )}
    </ProjectPageFrame>
  )
}
