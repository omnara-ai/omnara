import { isAgentActive } from '@omnara/react'
import type { OrgOverviewResponse } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'

import { AgentIcon } from '@/components/agents/AgentIcon'
import { AuroraPanel } from '@/components/aurora/AuroraPanel'
import {
  OverviewColumn,
  OverviewEmpty,
  OverviewList,
  OverviewRowLabel,
  overviewRowLimit,
  overviewRowLinkClass,
  OverviewRowTime,
} from '@/components/overview/OverviewColumn'
import { agentIcon, profileIcon } from '@/lib/agent-icon'

export function OverviewSummary({ overview }: { overview: OrgOverviewResponse }) {
  return (
    // A translucent card lit by the page's backdrop, like the usage panel below it.
    <AuroraPanel className="grid w-full gap-8 p-3 sm:p-4 md:grid-cols-2 md:gap-10">
      <LatestAgentsColumn overview={overview} />
      <EditedProfilesColumn overview={overview} />
    </AuroraPanel>
  )
}

function LatestAgentsColumn({ overview }: { overview: OrgOverviewResponse }) {
  const agents = overview.recent_agents.slice(0, overviewRowLimit)
  return (
    <OverviewColumn title="Recent agents">
      {agents.length === 0 ? (
        <OverviewEmpty message="No agents yet" />
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
                  className="size-6"
                />
                <OverviewRowLabel name={agent.name === '' ? 'Agent' : agent.name} />
                <OverviewRowTime value={agent.updated_at} />
              </Link>
            </li>
          ))}
        </OverviewList>
      )}
    </OverviewColumn>
  )
}

function EditedProfilesColumn({ overview }: { overview: OrgOverviewResponse }) {
  const profiles = overview.recent_agent_profiles.slice(0, overviewRowLimit)
  return (
    <OverviewColumn title="Recent profiles">
      {profiles.length === 0 ? (
        <OverviewEmpty message="No profiles yet" />
      ) : (
        <OverviewList>
          {profiles.map((profile) => (
            <li key={profile.id} className="min-w-0">
              <Link
                to="/projects/$projectId/agent-profiles/$profileId"
                params={{ projectId: profile.project_id, profileId: profile.id }}
                className={overviewRowLinkClass}
              >
                <AgentIcon icon={profileIcon(profile.id)} className="size-6" />
                <OverviewRowLabel name={profile.name} />
                <OverviewRowTime value={profile.updated_at} />
              </Link>
            </li>
          ))}
        </OverviewList>
      )}
    </OverviewColumn>
  )
}
