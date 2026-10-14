import { useAgentProfileQuery } from '@omnara/react'
import type { Agent, VisibleProject } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import type { ReactNode } from 'react'

import { AgentIcon } from '@/components/agents/AgentIcon'
import { ProjectTag } from '@/components/agents/ProjectTag'
import { ChevronRight } from '@/components/icons'
import { profileIcon } from '@/lib/agent-icon'

/**
 * Where an agent comes from: its project (when listed across projects) and the profile it
 * was launched from, each linking to its page.
 */
export function AgentOrigin({
  orgId,
  agent,
  project,
  showProfile,
  after,
}: {
  orgId: string
  agent: Agent
  project?: VisibleProject
  showProfile: boolean
  /** Follows the origin, separated from it only when the origin shows. */
  after?: ReactNode
}) {
  const profileId = showProfile ? agent.agent_profile_id : undefined
  const profile = useAgentProfileQuery(orgId, agent.project_id, profileId)
  const profileName = profile.data?.name
  if (!project && !profileName) return after ?? null
  return (
    <>
      <span className="text-muted-foreground inline-flex min-w-0 shrink items-center gap-1 text-xs">
        {project && <ProjectTag project={project} />}
        {project && profileName && (
          <ChevronRight aria-hidden="true" className="size-3 shrink-0 opacity-60" />
        )}
        {profileId && profileName && (
          <Link
            to="/projects/$projectId/agent-profiles/$profileId"
            params={{ projectId: agent.project_id, profileId }}
            className="hover:text-foreground relative inline-flex min-w-0 shrink items-center gap-1 hover:underline"
          >
            <AgentIcon icon={profileIcon(profileId)} className="size-3.5 shrink-0 rounded-[2px]" />
            <span className="truncate">{profileName}</span>
          </Link>
        )}
      </span>
      {after && (
        <>
          <span aria-hidden="true">·</span>
          {after}
        </>
      )}
    </>
  )
}
