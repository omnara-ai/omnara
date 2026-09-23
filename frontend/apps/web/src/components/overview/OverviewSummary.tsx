import { useOrgOverviewActivity } from '@omnara/react'
import type { OrgOverviewResponse } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { startOfDay } from 'date-fns'
import { type ReactNode, useState } from 'react'

import { OverviewSectionHeader } from '@/components/overview/OverviewSectionHeader'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { formatCompactCount, formatCount } from '@/lib/format'
import { errorMessage } from '@/lib/submit-status'
import { cn } from '@/lib/utils'

const recentLimit = 5
const rowLinkClass = 'group flex h-full min-w-0 items-center gap-3 text-sm'
const rowMetaClass = 'text-muted-foreground shrink-0 text-xs tabular-nums'

export function OverviewSummary({
  orgId,
  overview,
  overviewError,
  onRetry,
}: {
  orgId: string
  overview: OrgOverviewResponse | undefined
  overviewError: unknown
  onRetry: () => void
}) {
  return (
    <section className="flex flex-col gap-6">
      <OverviewSectionHeader title="Overview" />
      <div className="grid gap-4 md:grid-cols-3">
        <TodayColumn orgId={orgId} />
        {overview ? (
          <>
            <EditedProfilesColumn overview={overview} />
            <LatestAgentsColumn overview={overview} />
          </>
        ) : (
          <Card className="items-start gap-3 p-5 md:col-span-2">
            <p className="text-destructive text-sm" role="alert">
              {errorMessage(overviewError, 'Could not load agents and profiles.')}
            </p>
            <Button size="sm" variant="outline" onClick={onRetry}>
              Retry
            </Button>
          </Card>
        )}
      </div>
    </section>
  )
}

function TodayColumn({ orgId }: { orgId: string }) {
  const [since] = useState(() => startOfDay(new Date()).toISOString())
  const query = useOrgOverviewActivity(orgId, since)
  const stats = [
    { label: 'Agents created', value: query.data && formatCount(query.data.agents_created) },
    { label: 'Messages sent', value: query.data && formatCount(query.data.messages_sent) },
    { label: 'Tokens used', value: query.data && formatCompactCount(query.data.tokens_used) },
  ]
  return (
    <OverviewColumn title="Today">
      {query.isError ? (
        <p className="text-destructive text-sm" role="alert">
          {errorMessage(query.error, 'Could not load today’s activity.')}
        </p>
      ) : (
        <dl className="flex flex-1 flex-col justify-between gap-3">
          {stats.map((stat) => (
            <div key={stat.label} className="flex flex-col gap-0.5">
              <dt className="text-muted-foreground text-sm">{stat.label}</dt>
              <dd className="text-2xl leading-8 tracking-[-0.02em]">
                {stat.value ?? <Skeleton className="h-8 w-16" />}
              </dd>
            </div>
          ))}
        </dl>
      )}
    </OverviewColumn>
  )
}

function EditedProfilesColumn({ overview }: { overview: OrgOverviewResponse }) {
  const profiles = overview.recent_agent_profiles.slice(0, recentLimit)
  const agentCounts = new Map(
    overview.referenced_agent_profiles.map((profile) => [profile.id, profile.agent_count]),
  )
  return (
    <OverviewColumn title="Recently edited profiles">
      {profiles.length === 0 ? (
        <p className="text-muted-foreground text-sm">No profiles yet</p>
      ) : (
        <OverviewList>
          {profiles.map((profile) => (
            <li key={profile.id} className="min-w-0">
              <Link
                to="/projects/$projectId/agent-profiles/$profileId"
                params={{ projectId: profile.project_id, profileId: profile.id }}
                className={rowLinkClass}
              >
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
  const agents = overview.recent_agents.slice(0, recentLimit)
  const profileNames = new Map(
    overview.referenced_agent_profiles.map((profile) => [profile.id, profile.name]),
  )
  return (
    <OverviewColumn title="Latest agents">
      {agents.length === 0 ? (
        <p className="text-muted-foreground text-sm">No agents yet</p>
      ) : (
        <OverviewList>
          {agents.map((agent) => (
            <li key={agent.id} className="min-w-0">
              <Link
                to="/projects/$projectId/agents/$agentId/events"
                params={{ projectId: agent.project_id, agentId: agent.id }}
                className={rowLinkClass}
              >
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

function OverviewColumn({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Card className="min-w-0 gap-4 p-5">
      <h3 className="text-foreground/80 flex h-6 items-center text-sm font-medium">{title}</h3>
      {children}
    </Card>
  )
}

function OverviewList({ children }: { children: ReactNode }) {
  return <ul className="grid flex-1 grid-rows-5 gap-1.5">{children}</ul>
}

function OverviewRowLabel({ name, subtitle }: { name: string; subtitle?: string }) {
  return (
    <span className="flex min-w-0 flex-1 flex-col">
      <span className="truncate underline-offset-2 group-hover:underline">{name}</span>
      {subtitle && <span className="text-muted-foreground truncate text-xs">{subtitle}</span>}
    </span>
  )
}

function formatAgentCount(count: number) {
  if (count === 0) return 'No agents'
  return `${formatCount(count)} ${count === 1 ? 'agent' : 'agents'}`
}

function OverviewRowTime({ value }: { value: string }) {
  return <span className={cn(rowMetaClass, 'w-16 text-right')}>{formatTimeAgo(value)}</span>
}

const timeAgoFormatter = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
})

function formatTimeAgo(value: string) {
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
  return timeAgoFormatter.format(date)
}
