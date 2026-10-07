import { useIntegrations } from '@omnara/react'
import type { VisibleProject } from '@omnara/sdk'
import { Link, Navigate } from '@tanstack/react-router'

import { agentCardLinkClass, AgentCardTime } from '@/components/agents/AgentCardList'
import { integrationKindLabel } from '@/components/integrations/integrationDefinitions'
import { IntegrationIcon } from '@/components/integrations/IntegrationIcon'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { NewProjectButton } from '@/components/projects/NewProjectButton'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { useProjectDirectory } from '@/hooks/use-project-directory'
import { formatCount } from '@/lib/format'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

/**
 * Integrations live in a project, so the org-level entry asks which project first. With a
 * single project there's nothing to choose, so it goes straight there.
 */
const gridClass = 'grid gap-3 sm:grid-cols-2 lg:grid-cols-3'

export function OrgIntegrationsPage() {
  const { activeOrg } = useActiveOrg()
  const directory = useProjectDirectory(activeOrg.id)
  const projects = [...directory.projects.values()]
    .filter((project) => project.access.can_read)
    .sort((left, right) => left.name.localeCompare(right.name))
  const [only, second] = projects

  if (directory.isLoaded && only && !second) {
    return (
      <Navigate to="/projects/$projectId/integrations" params={{ projectId: only.id }} replace />
    )
  }

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-3">
      <SearchHeader
        title="Integrations"
        description="Integrations belong to a project. Choose a project to see and set up its integrations."
      />
      {!directory.isLoaded ? (
        <div className={gridClass}>
          {[0, 1, 2].map((index) => (
            <Skeleton key={index} className="h-32 rounded-xl" />
          ))}
        </div>
      ) : projects.length === 0 ? (
        <Empty className="rounded-xl border">
          <EmptyHeader>
            <EmptyDescription>
              No projects yet. Create a project to add integrations to it.
            </EmptyDescription>
          </EmptyHeader>
          {canManageOrg(activeOrg.role) && (
            <EmptyContent>
              <NewProjectButton orgId={activeOrg.id} label="Create a project" />
            </EmptyContent>
          )}
        </Empty>
      ) : (
        <ul className={gridClass}>
          {projects.map((project) => (
            <li key={project.id}>
              <ProjectIntegrationsCard orgId={activeOrg.id} project={project} />
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/** A project tile to pick, with the integrations it already has so the right one is easy to spot. */
function ProjectIntegrationsCard({ orgId, project }: { orgId: string; project: VisibleProject }) {
  const query = useIntegrations(orgId, project.id)
  const firstPage = query.data?.pages[0]?.data ?? []
  const kinds = [...new Set(firstPage.map((integration) => integration.integration_kind))]
  let count = '—'
  if (!query.isPending && !query.isError) {
    count = `${formatCount(firstPage.length)}${query.hasNextPage ? '+' : ''}`
  }
  const noun = firstPage.length === 1 && !query.hasNextPage ? 'integration' : 'integrations'
  return (
    <article className="bg-card hover:border-foreground/20 has-[a:focus-visible]:ring-ring/50 relative flex h-full flex-col gap-4 rounded-xl border p-4 transition-colors has-[a:focus-visible]:ring-[3px]">
      <div className="flex min-w-0 items-center gap-3">
        <div className="flex min-w-0 flex-col gap-0.5">
          <Link
            to="/projects/$projectId/integrations"
            params={{ projectId: project.id }}
            className={agentCardLinkClass}
          >
            {project.name}
          </Link>
          <AgentCardTime label="Updated" value={project.updated_at} />
        </div>
      </div>
      <div className="text-muted-foreground mt-auto flex min-h-5 items-center justify-between gap-2 text-xs">
        {kinds.length > 0 ? (
          <span
            className="flex items-center gap-1.5"
            title={kinds.map(integrationKindLabel).join(', ')}
          >
            {kinds.map((kind) => (
              <IntegrationIcon key={kind} integrationKind={kind} className="size-4" />
            ))}
          </span>
        ) : (
          <span>{query.isPending ? '' : 'No integrations yet'}</span>
        )}
        <span className="tabular-nums">
          {count} {noun}
        </span>
      </div>
    </article>
  )
}
