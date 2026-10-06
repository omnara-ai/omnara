import { isAgentActive, useAgentQuery } from '@omnara/react'
import type { Agent } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'

import { AgentIcon } from '@/components/agents/AgentIcon'
import { agentIcon, profileIcon } from '@/lib/agent-icon'
import { cn } from '@/lib/utils'

const referenceLinkClass = 'flex min-w-0 items-center gap-1.5 hover:underline'
const referenceIconClass = 'size-4 rounded-[2px]'

export function ProfileReferenceLink({
  projectId,
  profileId,
  name,
  className,
}: {
  projectId: string
  profileId: string
  name: string
  className?: string
}) {
  return (
    <Link
      to="/projects/$projectId/agent-profiles/$profileId"
      params={{ projectId, profileId }}
      className={cn(referenceLinkClass, className)}
    >
      <AgentIcon icon={profileIcon(profileId)} className={referenceIconClass} />
      <span className="truncate">{name}</span>
    </Link>
  )
}

export function AgentReferenceLink({ agent, name }: { agent: Agent; name: string }) {
  return (
    <Link
      to="/projects/$projectId/agents/$agentId"
      params={{ projectId: agent.project_id, agentId: agent.id }}
      className={referenceLinkClass}
    >
      <AgentIcon
        icon={agentIcon(agent.agent_profile_id, agent.id)}
        animated={isAgentActive(agent)}
        className={referenceIconClass}
      />
      <span className="truncate">{name}</span>
    </Link>
  )
}

export function FetchedAgentReferenceLink({
  orgId,
  projectId,
  agentId,
}: {
  orgId: string
  projectId: string
  agentId: string
}) {
  const { data } = useAgentQuery(orgId, projectId, agentId)
  if (!data) {
    return (
      <Link
        to="/projects/$projectId/agents/$agentId"
        params={{ projectId, agentId }}
        className="truncate font-mono text-xs hover:underline"
      >
        {agentId}
      </Link>
    )
  }
  return <AgentReferenceLink agent={data.agent} name={data.agent.name || 'Agent'} />
}
