import { useIntegrations } from '@omnara/react'
import { githubIntegrationSettings, type Integration } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'

import { SearchHeader } from '@/components/layout/SearchHeader'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

import { IntegrationCatalog } from './IntegrationCatalog'
import { integrationKindLabel } from './integrationDefinitions'
import { IntegrationIcon } from './IntegrationIcon'

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
    <>
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
      {query.isPending ? (
        <Spinner className="size-4" />
      ) : query.isError ? (
        <div role="alert" className="flex items-center gap-3 text-sm">
          Could not load integrations.
          <Button variant="outline" size="sm" onClick={() => void query.refetch()}>
            Retry
          </Button>
        </div>
      ) : (
        <div className="flex flex-col gap-3">
          {integrations.length === 0 && canManage ? (
            <IntegrationCatalog orgId={orgId} projectId={projectId} />
          ) : integrations.length === 0 ? (
            <p className="text-muted-foreground rounded-lg border border-dashed p-8 text-center text-sm">
              No integrations yet. Ask a project administrator to add one.
            </p>
          ) : (
            <ul className="divide-y rounded-lg border">
              {integrations.map((integration) => (
                <li key={integration.id}>
                  <Link
                    to="/projects/$projectId/integrations/$integrationId"
                    params={{ projectId, integrationId: integration.id }}
                    className="hover:bg-muted/40 flex flex-wrap items-center justify-between gap-3 px-4 py-4"
                  >
                    <div className="flex min-w-0 items-center gap-3">
                      <IntegrationIcon integrationKind={integration.integration_kind} />
                      <div className="flex min-w-0 flex-col gap-1">
                        <span className="truncate font-medium">{integration.name}</span>
                        <span className="text-muted-foreground text-sm">
                          {launchSummary(integration)}
                        </span>
                      </div>
                    </div>
                    <div className="flex items-center gap-2">
                      <Badge variant="outline">
                        {integrationKindLabel(integration.integration_kind)}
                      </Badge>
                      <Badge variant={integration.state === 'active' ? 'outline' : 'secondary'}>
                        {integration.state === 'active' ? 'Connected' : 'Disconnected'}
                      </Badge>
                    </div>
                  </Link>
                </li>
              ))}
            </ul>
          )}
          {query.hasNextPage && (
            <Button
              className="self-start"
              variant="outline"
              size="sm"
              disabled={query.isFetchingNextPage}
              onClick={() => void query.fetchNextPage()}
            >
              Load more integrations
            </Button>
          )}
        </div>
      )}
    </>
  )
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
