import {
  useDeleteProjectApp,
  useIntegrationConnection,
  useProjectApps,
  useUpdateProjectApp,
} from '@omnara/react'
import { ApiError, type ProjectApp } from '@omnara/sdk'
import { useState } from 'react'

import { Trash2 } from '@/components/icons'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { formatDateTime } from '@/lib/format'

import { ProjectAppConnections } from './ProjectAppConnections'
import { ProjectAppProfilesDialog } from './ProjectAppProfilesDialog'

const definitionLabels = new Map([
  ['omnara.slack', 'Slack'],
  ['omnara.github', 'GitHub'],
  ['omnara.discord', 'Discord'],
])

export function AgentProfileApps({
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
  const query = useProjectApps(orgId, projectId)
  const [showAll, setShowAll] = useState(false)
  const [editingApp, setEditingApp] = useState<ProjectApp>()
  const apps = useInfiniteQueryItems(query).filter(
    (app) =>
      showAll || app.settings.launcher?.slots.some((slot) => slot.agent_profile_id === profileId),
  )
  const deleteApp = useDeleteProjectApp(orgId, projectId)
  const updateApp = useUpdateProjectApp(orgId, projectId)

  return (
    <div className="flex flex-col gap-2">
      <label className="flex gap-2 text-sm">
        <input
          type="checkbox"
          checked={showAll}
          onChange={(e) => {
            setShowAll(e.target.checked)
          }}
        />
        Show all project apps, including setups without this profile
      </label>
      {query.isPending ? (
        <Spinner className="size-4" />
      ) : query.isError ? (
        <div className="flex items-center gap-3">
          <p className="text-muted-foreground text-sm">Couldn&rsquo;t load apps.</p>
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
      ) : apps.length === 0 ? (
        <div className="border-border bg-background/60 text-muted-foreground flex min-h-16 items-center justify-center rounded-md border border-dashed px-4 text-sm">
          {query.hasNextPage
            ? 'No apps for this profile on the loaded pages.'
            : 'No apps launch this profile.'}
        </div>
      ) : (
        <ul className="bg-background flex flex-col divide-y rounded-md border">
          {apps.map((app) => (
            <li
              key={app.id}
              className="flex flex-col gap-2 px-3 py-2 sm:flex-row sm:items-center sm:justify-between sm:gap-3"
            >
              <div className="flex min-w-0 items-center gap-2 text-sm">
                <span className="truncate font-medium">{app.name}</span>
                <Badge variant="outline">
                  {definitionLabels.get(app.settings.resource.definition ?? '') ?? 'App'}
                </Badge>
                {!app.enabled && <Badge variant="secondary">Disabled</Badge>}
                <AppConnectionState
                  orgId={orgId}
                  projectId={projectId}
                  connectionId={app.settings.resource.connection ?? ''}
                />
              </div>
              <div className="flex min-w-0 items-center justify-between gap-3 sm:shrink-0 sm:justify-start">
                <span className="text-muted-foreground text-xs">
                  Created {formatDateTime(app.created_at)}
                </span>
                {canManage &&
                  app.settings.launcher &&
                  ['omnara.slack', 'omnara.discord'].includes(
                    app.settings.resource.definition ?? '',
                  ) && (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => {
                        setEditingApp(app)
                      }}
                    >
                      Edit profiles
                    </Button>
                  )}
                {canManage && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={updateApp.isPending}
                    onClick={() => {
                      updateApp.mutate(
                        {
                          appID: app.id,
                          name: app.name,
                          settings: app.settings,
                          enabled: !app.enabled,
                        },
                        {
                          onError: (error) => {
                            window.alert(
                              error instanceof ApiError ? error.message : 'Could not update app',
                            )
                          },
                        },
                      )
                    }}
                  >
                    {app.enabled ? 'Disable app' : 'Enable app'}
                  </Button>
                )}
                {canManage && (
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={deleteApp.isPending}
                    onClick={() => {
                      const confirmed = window.confirm(
                        `Remove app ${app.name}? ` +
                          'This stops its launcher for every profile in this setup. Existing agents and the provider connection are kept.',
                      )
                      if (!confirmed) return
                      deleteApp.mutate(app.id, {
                        onError: (error) => {
                          window.alert(
                            error instanceof ApiError ? error.message : 'Could not remove app',
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
      <ProjectAppConnections orgId={orgId} projectId={projectId} canManage={canManage} />
      {editingApp && (
        <ProjectAppProfilesDialog
          key={editingApp.id}
          orgId={orgId}
          projectId={projectId}
          app={editingApp}
          onOpenChange={(open) => {
            if (!open) setEditingApp(undefined)
          }}
        />
      )}
    </div>
  )
}

function AppConnectionState({
  orgId,
  projectId,
  connectionId,
}: {
  orgId: string
  projectId: string
  connectionId: string
}) {
  const query = useIntegrationConnection(orgId, projectId, connectionId)
  if (!connectionId) return null
  if (query.isPending)
    return <span className="text-muted-foreground text-xs">Checking connection…</span>
  if (query.isError) return <Badge variant="destructive">Connection unavailable</Badge>
  return (
    <Badge variant={query.data.state === 'active' ? 'outline' : 'secondary'}>
      Connection {query.data.state}
    </Badge>
  )
}
