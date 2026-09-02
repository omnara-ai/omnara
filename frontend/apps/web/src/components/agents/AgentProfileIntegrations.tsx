import { useDeleteIntegrationInstall, useIntegrationInstalls } from '@omnara/react'
import { ApiError, type IntegrationInstall } from '@omnara/sdk'

import { Trash2 } from '@/components/icons'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { formatDateTime } from '@/lib/format'

function providerLabel(provider: string) {
  return provider === 'slack' ? 'Slack' : provider
}

function installName(install: IntegrationInstall) {
  return install.provider_agent_display_name || install.provider_account_ref
}

export function AgentProfileIntegrations({
  orgId,
  projectId,
  profileId,
  canManage,
}: {
  orgId: string
  projectId: string
  profileId: string
  canManage: boolean
}) {
  const query = useIntegrationInstalls(orgId, projectId, {
    filters: { agent_profile_id: profileId },
  })
  const installs = useInfiniteQueryItems(query)
  const deleteInstall = useDeleteIntegrationInstall(orgId, projectId)

  return (
    <div className="flex flex-col gap-2">
      {query.isPending ? (
        <Spinner className="size-4" />
      ) : query.isError ? (
        <div className="flex items-center gap-3">
          <p className="text-muted-foreground text-sm">Couldn&rsquo;t load integrations.</p>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              void query.refetch()
            }}
          >
            Retry
          </Button>
        </div>
      ) : installs.length === 0 ? (
        <div className="border-border bg-background/60 text-muted-foreground flex min-h-16 items-center justify-center rounded-md border border-dashed px-4 text-sm">
          No integrations yet.
          {canManage && ' Use “Add integration” to make this profile available in an external app.'}
        </div>
      ) : (
        <ul className="bg-background flex flex-col divide-y rounded-md border">
          {installs.map((install) => (
            <li
              key={install.id}
              className="flex flex-col gap-2 px-3 py-2 sm:flex-row sm:items-center sm:justify-between sm:gap-3"
            >
              <div className="flex min-w-0 items-center gap-2 text-sm">
                <span className="truncate font-medium">{installName(install)}</span>
                <Badge variant="outline">{providerLabel(install.provider)}</Badge>
                {install.state === 'disabled' && <Badge variant="secondary">Disabled</Badge>}
              </div>
              <div className="flex min-w-0 items-center justify-between gap-3 sm:shrink-0 sm:justify-start">
                <span className="text-muted-foreground text-xs">
                  Installed {formatDateTime(install.created_at)}
                </span>
                {canManage && (
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={deleteInstall.isPending}
                    onClick={() => {
                      const confirmed = window.confirm(
                        `Remove the ${providerLabel(install.provider)} integration ${installName(install)}? ` +
                          'Agents are kept, but the connected app stops working.',
                      )
                      if (!confirmed) return
                      deleteInstall.mutate(install.id, {
                        onError: (error) => {
                          window.alert(
                            error instanceof ApiError
                              ? error.message
                              : 'Could not remove integration',
                          )
                        },
                      })
                    }}
                  >
                    <Trash2 />
                    Remove
                  </Button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
      {query.hasNextPage && (
        <Button
          size="sm"
          variant="outline"
          className="self-start"
          disabled={query.isFetchingNextPage}
          onClick={() => {
            void query.fetchNextPage()
          }}
        >
          Show more
        </Button>
      )}
    </div>
  )
}
