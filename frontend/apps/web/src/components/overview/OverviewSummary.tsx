import { isAgentActive } from '@omnara/react'
import type { OrgOverviewResponse } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'

import { AgentIcon } from '@/components/agents/AgentIcon'
import { OrgCreateAgentProfileButton } from '@/components/agents/CreateAgentProfileButton'
import {
  OverviewColumn,
  OverviewEmpty,
  OverviewList,
  OverviewRowLabel,
  overviewRowLimit,
  overviewRowLinkClass,
  OverviewRowTime,
} from '@/components/overview/OverviewColumn'
import { OverviewSectionHeader } from '@/components/overview/OverviewSectionHeader'
import { NewProjectButton } from '@/components/projects/NewProjectButton'
import { Button } from '@/components/ui/button'
import { agentIcon, profileIcon } from '@/lib/agent-icon'
import { formatCount } from '@/lib/format'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function OverviewSummary({ overview }: { overview: OrgOverviewResponse }) {
  return (
    <section className="flex flex-col gap-6">
      <OverviewSectionHeader title="Overview" />
      <div className="grid gap-4 md:grid-cols-3">
        <RecentProjectsColumn overview={overview} />
        <EditedProfilesColumn overview={overview} />
        <LatestAgentsColumn overview={overview} />
      </div>
    </section>
  )
}

function RecentProjectsColumn({ overview }: { overview: OrgOverviewResponse }) {
  const { activeOrg } = useActiveOrg()
  const projects = recentProjects(overview)
  return (
    <OverviewColumn title="Recent projects">
      {projects.length === 0 ? (
        <OverviewEmpty
          message="No projects yet"
          action={canManageOrg(activeOrg.role) && <NewProjectButton orgId={activeOrg.id} />}
        />
      ) : (
        <OverviewList>
          {projects.map(({ project, activeAt }) => (
            <li key={project.id} className="min-w-0">
              <Link
                to="/projects/$projectId"
                params={{ projectId: project.id }}
                className={overviewRowLinkClass}
              >
                <OverviewRowLabel name={project.name} />
                <OverviewRowTime value={activeAt} />
              </Link>
            </li>
          ))}
        </OverviewList>
      )}
    </OverviewColumn>
  )
}

function recentProjects(overview: OrgOverviewResponse) {
  const activity = new Map<string, number>()
  const record = (projectId: string, timestamp: string) => {
    const time = Date.parse(timestamp)
    if (time > (activity.get(projectId) ?? Number.NEGATIVE_INFINITY)) activity.set(projectId, time)
  }
  for (const project of overview.projects) record(project.id, project.updated_at)
  for (const entry of overview.project_activity) record(entry.project_id, entry.last_active_at)
  return overview.projects
    .map((project) => ({ project, time: activity.get(project.id) ?? 0 }))
    .sort((left, right) => right.time - left.time)
    .slice(0, overviewRowLimit)
    .map(({ project, time }) => ({ project, activeAt: new Date(time).toISOString() }))
}

function EditedProfilesColumn({ overview }: { overview: OrgOverviewResponse }) {
  const { activeOrg } = useActiveOrg()
  const profiles = overview.recent_agent_profiles.slice(0, overviewRowLimit)
  const agentCounts = new Map(
    overview.referenced_agent_profiles.map((profile) => [profile.id, profile.agent_count]),
  )
  return (
    <OverviewColumn title="Recently edited profiles">
      {profiles.length === 0 ? (
        <OverviewEmpty
          message="No profiles yet"
          action={
            <OrgCreateAgentProfileButton
              orgId={activeOrg.id}
              offerNewProject={canManageOrg(activeOrg.role)}
            />
          }
        />
      ) : (
        <OverviewList>
          {profiles.map((profile) => (
            <li key={profile.id} className="min-w-0">
              <Link
                to="/projects/$projectId/agent-profiles/$profileId"
                params={{ projectId: profile.project_id, profileId: profile.id }}
                className={overviewRowLinkClass}
              >
                <AgentIcon icon={profileIcon(profile.id)} className="size-8" />
                <OverviewRowLabel
                  name={profile.name}
                  subtitle={formatAgentCount(agentCounts.get(profile.id) ?? 0)}
                />
                <OverviewRowTime value={profile.updated_at} />
              </Link>
            </li>
          ))}
        </OverviewList>
      )}
    </OverviewColumn>
  )
}

function LatestAgentsColumn({ overview }: { overview: OrgOverviewResponse }) {
  const { activeOrg } = useActiveOrg()
  const agents = overview.recent_agents.slice(0, overviewRowLimit)
  const profileNames = new Map(
    overview.referenced_agent_profiles.map((profile) => [profile.id, profile.name]),
  )
  return (
    <OverviewColumn title="Latest agents">
      {agents.length === 0 ? (
        <OverviewEmpty
          message="No agents yet"
          action={
            overview.recent_agent_profiles.length === 0 ? (
              <OrgCreateAgentProfileButton
                orgId={activeOrg.id}
                offerNewProject={canManageOrg(activeOrg.role)}
              />
            ) : (
              <Button asChild size="sm">
                <Link to="/agents">Launch an agent</Link>
              </Button>
            )
          }
        />
      ) : (
        <OverviewList>
          {agents.map((agent) => (
            <li key={agent.id} className="min-w-0">
              <Link
                to="/projects/$projectId/agents/$agentId/events"
                params={{ projectId: agent.project_id, agentId: agent.id }}
                className={overviewRowLinkClass}
              >
                <AgentIcon
                  icon={agentIcon(agent.agent_profile_id, agent.id)}
                  animated={isAgentActive(agent)}
                  className="size-8"
                />
                <OverviewRowLabel
                  name={agent.name === '' ? 'Agent' : agent.name}
                  subtitle={agent.agent_profile_id && profileNames.get(agent.agent_profile_id)}
                />
                <OverviewRowTime value={agent.updated_at} />
              </Link>
            </li>
          ))}
        </OverviewList>
      )}
    </OverviewColumn>
  )
}

function formatAgentCount(count: number) {
  if (count === 0) return 'No agents'
  return `${formatCount(count)} ${count === 1 ? 'agent' : 'agents'}`
}
