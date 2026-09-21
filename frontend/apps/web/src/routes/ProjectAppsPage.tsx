import { Link } from '@tanstack/react-router'

import { ProjectAppsList } from '@/components/apps/ProjectAppsList'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { Button } from '@/components/ui/button'

export function ProjectAppsPage() {
  return (
    <ProjectPageFrame title="Apps">
      {({ activeOrg, projectId, project }) => (
        <>
          <header className="flex items-start justify-between gap-4">
            <div className="flex flex-col gap-2">
              <h1 className="type-title">Apps</h1>
              <p className="text-muted-foreground text-sm">
                Connect services, choose how agents start, and configure the capabilities they use.
              </p>
            </div>
            {project?.access.can_manage && (
              <Button asChild>
                <Link to="/projects/$projectId/apps/new" params={{ projectId }}>
                  Add app
                </Link>
              </Button>
            )}
          </header>
          <ProjectAppsList
            orgId={activeOrg.id}
            projectId={projectId}
            canManage={project?.access.can_manage ?? false}
          />
        </>
      )}
    </ProjectPageFrame>
  )
}
