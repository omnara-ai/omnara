import { useAgentProfileQuery } from '@omnara/react'
import { Link } from '@tanstack/react-router'

import { AgentIcon } from '@/components/agents/AgentIcon'
import { profileIcon } from '@/lib/agent-icon'

/** The agent profiles an integration launches, shown inside its expanded card. */
export function IntegrationProfileList({
  orgId,
  projectId,
  profileIds,
  open,
}: {
  orgId: string
  projectId: string
  profileIds: string[]
  /** Profile names load only once the card is opened. */
  open: boolean
}) {
  return (
    <div className="flex flex-col gap-1 border-t px-2 py-2">
      <ul className="flex flex-col gap-0.5">
        {profileIds.map((profileId) => (
          <ProfileRow
            key={profileId}
            orgId={orgId}
            projectId={projectId}
            profileId={profileId}
            open={open}
          />
        ))}
      </ul>
    </div>
  )
}

function ProfileRow({
  orgId,
  projectId,
  profileId,
  open,
}: {
  orgId: string
  projectId: string
  profileId: string
  open: boolean
}) {
  const query = useAgentProfileQuery(orgId, projectId, open ? profileId : undefined)
  const profile = query.data
  return (
    <li className="flex min-h-9 min-w-0 items-center gap-2.5 rounded-md px-2 py-1.5 text-sm">
      <AgentIcon icon={profileIcon(profileId)} className="size-4 rounded-[2px]" />
      {profile ? (
        <Link
          to="/projects/$projectId/agent-profiles/$profileId"
          params={{ projectId, profileId }}
          className="truncate font-medium hover:underline"
        >
          {profile.name}
        </Link>
      ) : (
        <span className="text-muted-foreground truncate">
          {query.isError ? 'Unavailable profile' : 'Loading…'}
        </span>
      )}
    </li>
  )
}
