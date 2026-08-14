import type { OrgOverviewResponse, VisibleProject } from '@omnara/sdk'
import { Link, useNavigate } from '@tanstack/react-router'
import { ChevronDownIcon, PlusIcon } from 'lucide-react'

import { DataTable } from '@/components/data-table/DataTable'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

export function RecentAgentsSection({
  overview,
  isError,
  onRetry,
}: {
  overview?: OrgOverviewResponse
  isError: boolean
  onRetry: () => void
}) {
  const navigate = useNavigate()
  const projects = overview?.projects ?? []
  const projectNames = new Map(projects.map((project) => [project.id, project.name]))
  const agents = overview?.recent_agents ?? []
  const profiles = overview?.recent_agent_profiles ?? []
  const showProfiles = !isError && agents.length === 0 && profiles.length > 0

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-col gap-1">
          <h2 className="text-2xl font-bold tracking-tight">
            {showProfiles ? 'Recent profiles' : 'Recent agents'}
          </h2>
          {showProfiles && (
            <p className="text-muted-foreground text-sm">
              No agents running right now. Open a profile to launch one.
            </p>
          )}
        </div>
        <NewAgentButton projects={projects} />
      </div>
      {showProfiles ? (
        <DataTable
          columns={[
            {
              id: 'profile',
              header: 'Profile',
              cell: (profile) => <span className="font-medium">{profile.name}</span>,
            },
            {
              id: 'project',
              header: 'Project',
              cell: (profile) => (
                <span className="text-muted-foreground">
                  {projectNames.get(profile.project_id) ?? '—'}
                </span>
              ),
            },
            {
              id: 'model',
              header: 'Model',
              cell: (profile) => (
                <span className="truncate">{profile.current_config.model.name}</span>
              ),
            },
            {
              id: 'updated',
              header: 'Updated',
              className: 'w-36',
              cell: (profile) => (
                <span className="text-muted-foreground">
                  {formatLastActive(profile.updated_at)}
                </span>
              ),
            },
          ]}
          data={profiles}
          getRowId={(profile) => profile.id}
          onRowClick={(profile) => {
            void navigate({
              to: '/projects/$projectId/agent-profiles/$profileId',
              params: { projectId: profile.project_id, profileId: profile.id },
            })
          }}
          emptyMessage="No profiles yet."
        />
      ) : (
        <DataTable
          columns={[
            {
              id: 'agent',
              header: 'Agent',
              cell: (agent) => (
                <span className="font-medium">{agent.name === '' ? 'Agent' : agent.name}</span>
              ),
            },
            {
              id: 'project',
              header: 'Project',
              cell: (agent) => (
                <span className="text-muted-foreground">
                  {projectNames.get(agent.project_id) ?? '—'}
                </span>
              ),
            },
            {
              id: 'model',
              header: 'Model',
              cell: (agent) =>
                agent.model ? (
                  <span className="truncate">{agent.model.name}</span>
                ) : (
                  <span className="text-muted-foreground">—</span>
                ),
            },
            {
              id: 'last-active',
              header: 'Last active',
              className: 'w-36',
              cell: (agent) => (
                <span className="text-muted-foreground">{formatLastActive(agent.updated_at)}</span>
              ),
            },
          ]}
          data={agents}
          getRowId={(agent) => agent.id}
          onRowClick={(agent) => {
            void navigate({
              to: '/projects/$projectId/agents/$agentId',
              params: { projectId: agent.project_id, agentId: agent.id },
            })
          }}
          isError={isError}
          onRetry={onRetry}
          emptyMessage="No agents yet. Create one to get started."
        />
      )}
    </div>
  )
}

function NewAgentButton({ projects }: { projects: VisibleProject[] }) {
  const manageable = projects.filter((project) => project.access.can_manage)
  if (manageable.length === 0) return null
  if (manageable.length === 1) {
    const project = manageable[0]
    if (project == null) return null
    return (
      <Button asChild size="sm">
        <Link to="/projects/$projectId/agents/new" params={{ projectId: project.id }}>
          <PlusIcon />
          New agent
        </Link>
      </Button>
    )
  }
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button size="sm">
          <PlusIcon />
          New agent
          <ChevronDownIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {manageable.map((project) => (
          <DropdownMenuItem key={project.id} asChild>
            <Link to="/projects/$projectId/agents/new" params={{ projectId: project.id }}>
              {project.name}
            </Link>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

const lastActiveFormatter = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
})

function formatLastActive(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  const seconds = Math.round((Date.now() - date.getTime()) / 1000)
  if (seconds < 60) return 'Just now'
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.round(hours / 24)
  if (days < 7) return `${days}d ago`
  return lastActiveFormatter.format(date)
}
