import { useIntegrations } from '@omnara/react'
import {
  githubIntegrationSettings,
  type Integration,
  profileIntegrationProfiles,
  type VisibleProject,
} from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { type ReactNode, useId, useState } from 'react'

import {
  AgentCard,
  AgentCardGlyph,
  agentCardLinkClass,
  AgentCardStat,
  AgentCardStatToggle,
  AgentCardTime,
} from '@/components/agents/AgentCardList'
import { ProjectTag } from '@/components/agents/ProjectTag'
import { Bot } from '@/components/icons'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { Button } from '@/components/ui/button'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { formatCount } from '@/lib/format'

import { IntegrationCatalog } from './IntegrationCatalog'
import { integrationKindLabel } from './integrationDefinitions'
import { IntegrationIcon } from './IntegrationIcon'
import { IntegrationProfileList } from './IntegrationProfileList'

export function IntegrationsList({
  orgId,
  projectId,
  canManage = false,
}: {
  orgId: string
  projectId: string
  canManage?: boolean
}) {
  const query = useIntegrations(orgId, projectId)
  const integrations = useInfiniteQueryItems(query)
  return (
    <div className="flex flex-col gap-3">
      <SearchHeader
        title="Integrations"
        description="Connect services, choose how agents start, and configure the capabilities they use."
      >
        {canManage && integrations.length > 0 && (
          <Button asChild size="sm">
            <Link to="/projects/$projectId/integrations/new" params={{ projectId }}>
              Add integration
            </Link>
          </Button>
        )}
      </SearchHeader>
      <IntegrationCardList
        orgId={orgId}
        query={query}
        integrations={integrations}
        empty={
          canManage ? (
            <IntegrationCatalog orgId={orgId} projectId={projectId} />
          ) : (
            <Empty className="rounded-xl border">
              <EmptyHeader>
                <EmptyDescription>
                  No integrations yet. Ask a project administrator to add one.
                </EmptyDescription>
              </EmptyHeader>
            </Empty>
          )
        }
      />
    </div>
  )
}

interface IntegrationListQuery {
  isPending: boolean
  isError: boolean
  hasNextPage: boolean
  isFetchingNextPage: boolean
  refetch: () => void
  fetchNextPage: () => void
}

/**
 * Loading, error, empty, and paged states for a list of integration cards. `projectOf` tags
 * each card with its project, for lists that span projects.
 */
export function IntegrationCardList({
  orgId,
  query,
  integrations,
  empty,
  projectOf,
}: {
  orgId: string
  query: IntegrationListQuery
  integrations: Integration[]
  empty: ReactNode
  projectOf?: (integration: Integration) => VisibleProject | undefined
}) {
  if (query.isPending)
    return (
      <div className="flex flex-col gap-5">
        {[0, 1, 2].map((index) => (
          <Skeleton key={index} className="h-[4.25rem] rounded-xl" />
        ))}
      </div>
    )
  if (query.isError)
    return (
      <Empty className="rounded-xl border">
        <EmptyHeader>
          <EmptyDescription role="alert">Could not load integrations.</EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              query.refetch()
            }}
          >
            Retry
          </Button>
        </EmptyContent>
      </Empty>
    )
  if (integrations.length === 0) return empty
  return (
    <div className="flex flex-col gap-3">
      <ul className="flex flex-col gap-5">
        {integrations.map((integration) => (
          <li key={integration.id}>
            <IntegrationCard
              orgId={orgId}
              integration={integration}
              project={projectOf?.(integration)}
            />
          </li>
        ))}
      </ul>
      {query.hasNextPage && (
        <Button
          className="self-start"
          variant="outline"
          size="sm"
          disabled={query.isFetchingNextPage}
          onClick={() => {
            query.fetchNextPage()
          }}
        >
          Load more integrations
        </Button>
      )}
    </div>
  )
}

function IntegrationCard({
  orgId,
  integration,
  project,
}: {
  orgId: string
  integration: Integration
  project?: VisibleProject
}) {
  const projectId = integration.project_id
  const [expanded, setExpanded] = useState(false)
  const expansionId = useId()
  const profileIds = launcherProfileIds(integration)
  const expansion = {
    id: expansionId,
    open: expanded,
    content: (
      <IntegrationProfileList
        orgId={orgId}
        projectId={projectId}
        profileIds={profileIds}
        open={expanded}
      />
    ),
  }
  const label = profileIds.length === 1 ? 'agent profile' : 'agent profiles'
  return (
    <AgentCard
      icon={
        <AgentCardGlyph>
          <IntegrationIcon integrationKind={integration.integration_kind} className="size-5" />
        </AgentCardGlyph>
      }
      title={
        <Link
          to="/projects/$projectId/integrations/$integrationId"
          params={{ projectId, integrationId: integration.id }}
          className={agentCardLinkClass}
        >
          {integration.name}
        </Link>
      }
      subtitle={
        <>
          <span className="shrink-0">{integrationKindLabel(integration.integration_kind)}</span>
          <span aria-hidden="true">·</span>
          <span className="truncate">{launchSummary(integration)}</span>
          {project && (
            <>
              <span aria-hidden="true">·</span>
              <ProjectTag project={project} />
            </>
          )}
        </>
      }
      meta={
        <>
          <IntegrationStatus integration={integration} />
          <AgentCardTime label="Updated" value={integration.updated_at} />
        </>
      }
      stats={
        profileIds.length === 0 ? (
          <AgentCardStat icon={Bot} label={label} value="0" />
        ) : (
          <AgentCardStatToggle
            icon={Bot}
            label={label}
            value={formatCount(profileIds.length)}
            expansion={expansion}
            onToggle={() => {
              setExpanded((open) => !open)
            }}
          />
        )
      }
      expansion={profileIds.length === 0 ? undefined : expansion}
    />
  )
}

function IntegrationStatus({ integration }: { integration: Integration }) {
  return (
    <span className="text-muted-foreground whitespace-nowrap text-xs">
      {integrationStatusLabel(integration)}
    </span>
  )
}

function integrationStatusLabel(integration: Integration) {
  if (integration.state !== 'active') return 'Disconnected'
  return integration.runtime_failure ? 'Connection failed' : 'Connected'
}

/** Profiles the integration's launcher starts; settings it can't parse count as none. */
function launcherProfileIds(integration: Integration) {
  try {
    return profileIntegrationProfiles(integration)
  } catch {
    return []
  }
}

function launchSummary(integration: Integration) {
  if (!integration.settings.launcher) return 'No event launcher'
  if (integration.integration_kind === 'github_pr') {
    const trigger = githubIntegrationSettings(integration.settings).launcher?.trigger
    if (trigger === 'both') return 'Starts agents on new pull requests or mentions'
    if (trigger === 'pull_request_opened') return 'Starts agents when a pull request opens'
  }
  return 'Starts agents when the bot is mentioned'
}
