import { useProjectApps } from '@omnara/react'
import { Link } from '@tanstack/react-router'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

import { appDefinitionLabel } from './appDefinitions'

export function ProjectAppsList({ orgId, projectId }: { orgId: string; projectId: string }) {
  const query = useProjectApps(orgId, projectId)
  const apps = useInfiniteQueryItems(query)
  if (query.isPending) return <Spinner className="size-4" />
  if (query.isError)
    return (
      <div role="alert" className="flex items-center gap-3 text-sm">
        Could not load apps.
        <Button variant="outline" size="sm" onClick={() => void query.refetch()}>
          Retry
        </Button>
      </div>
    )
  return (
    <div className="flex flex-col gap-3">
      {apps.length === 0 ? (
        <p className="text-muted-foreground rounded-lg border border-dashed p-8 text-center text-sm">
          No apps yet. Add an app to connect this project to Slack, Discord, or GitHub.
        </p>
      ) : (
        <ul className="divide-y rounded-lg border">
          {apps.map((app) => (
            <li key={app.id}>
              <Link
                to="/projects/$projectId/apps/$appId"
                params={{ projectId, appId: app.id }}
                className="hover:bg-muted/40 flex flex-wrap items-center justify-between gap-3 px-4 py-4"
              >
                <div className="flex min-w-0 flex-col gap-1">
                  <span className="truncate font-medium">{app.name}</span>
                  <span className="text-muted-foreground text-sm">
                    {app.settings.launcher
                      ? app.settings.launcher.trigger === 'pull_request_opened'
                        ? 'Starts agents when a pull request opens'
                        : 'Starts agents when the bot is mentioned'
                      : 'No automatic launches'}
                  </span>
                </div>
                <div className="flex items-center gap-2">
                  <Badge variant="outline">{appDefinitionLabel(app.definition_id)}</Badge>
                  <Badge variant={app.state === 'active' ? 'outline' : 'secondary'}>
                    {app.state === 'active' ? 'Connected' : 'Disconnected'}
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
          Load more apps
        </Button>
      )}
    </div>
  )
}
