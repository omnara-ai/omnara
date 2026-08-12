import { useOmnaraClient } from '@omnara/react'
import type { Agent, AgentProfile, VisibleProject } from '@omnara/sdk'
import { listAgentProfilesOptions, listAgentsOptions } from '@omnara/sdk/tanstack'
import { useQueries } from '@tanstack/react-query'
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

const maxProjects = 10
const rowLimit = 5

interface RecentAgentRow {
  agent: Agent
  project: VisibleProject
}

interface ProfileRow {
  profile: AgentProfile
  project: VisibleProject
}

export function RecentAgentsSection({
  orgId,
  projects,
}: {
  orgId: string
  projects: VisibleProject[]
}) {
  const client = useOmnaraClient()
  const navigate = useNavigate()
  const readableProjects = projects.filter((project) => project.access.can_read)
  const results = useQueries({
    queries: readableProjects.slice(0, maxProjects).map((project) =>
      listAgentsOptions({
        path: { orgID: orgId, projectID: project.id },
        query: { sort: '-updated_at' as const, limit: rowLimit },
        client,
      }),
    ),
  })

  const rows: RecentAgentRow[] = readableProjects
    .slice(0, maxProjects)
    .flatMap((project, index) => {
      const data = results[index]?.data
      if (!data) return []
      return data.data.map((agent) => ({ agent, project }))
    })
    .sort((a, b) => b.agent.updated_at.localeCompare(a.agent.updated_at))
    .slice(0, rowLimit)

  const isPending = results.some((result) => result.isPending)
  const isError = rows.length === 0 && results.some((result) => result.isError)

  const agentsEmpty = !isPending && !isError && rows.length === 0
  const profileResults = useQueries({
    queries: readableProjects.slice(0, maxProjects).map((project) => ({
      ...listAgentProfilesOptions({
        path: { orgID: orgId, projectID: project.id },
        query: { sort: '-updated_at' as const, limit: rowLimit },
        client,
      }),
      enabled: agentsEmpty,
    })),
  })
  const profileRows: ProfileRow[] = readableProjects
    .slice(0, maxProjects)
    .flatMap((project, index) => {
      const data = profileResults[index]?.data
      if (!data) return []
      return data.data.map((profile) => ({ profile, project }))
    })
    .sort((a, b) => b.profile.updated_at.localeCompare(a.profile.updated_at))
    .slice(0, rowLimit)
  const profilesPending = agentsEmpty && profileResults.some((result) => result.isPending)
  const showProfiles = agentsEmpty && !profilesPending && profileRows.length > 0

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
            { header: 'Profile' },
            { header: 'Project' },
            { header: 'Model' },
            { header: 'Updated', className: 'w-36' },
          ]}
          data={profileRows}
          getRowId={(row) => row.profile.id}
          rowCells={(row) => [
            <span className="font-medium">{row.profile.name}</span>,
            <span className="text-muted-foreground">{row.project.name}</span>,
            <span className="truncate">{row.profile.current_config.model.name}</span>,
            <span className="text-muted-foreground">
              {formatLastActive(row.profile.updated_at)}
            </span>,
          ]}
          onRowClick={(row) => {
            void navigate({
              to: '/projects/$projectId/agent-profiles/$profileId',
              params: { projectId: row.project.id, profileId: row.profile.id },
            })
          }}
          emptyMessage="No profiles yet."
        />
      ) : (
        <DataTable
          columns={[
            { header: 'Agent' },
            { header: 'Project' },
            { header: 'Model' },
            { header: 'Last active', className: 'w-36' },
          ]}
          data={rows}
          getRowId={(row) => row.agent.id}
          rowCells={(row) => [
            <span className="font-medium">{row.agent.name || 'Agent'}</span>,
            <span className="text-muted-foreground">{row.project.name}</span>,
            row.agent.model ? (
              <span className="truncate">{row.agent.model.name}</span>
            ) : (
              <span className="text-muted-foreground">—</span>
            ),
            <span className="text-muted-foreground">
              {formatLastActive(row.agent.updated_at)}
            </span>,
          ]}
          onRowClick={(row) => {
            void navigate({
              to: '/projects/$projectId/agents/$agentId',
              params: { projectId: row.project.id, agentId: row.agent.id },
            })
          }}
          isPending={isPending || profilesPending}
          isError={isError}
          onRetry={() => {
            for (const result of results) {
              if (result.isError) void result.refetch()
            }
          }}
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
  return new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' }).format(date)
}
