import { useAppDefinitions } from '@omnara/react'
import { Link } from '@tanstack/react-router'

import { ArrowRight } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'

import { appCatalog } from './appDefinitions'
import { AppIcon } from './AppIcon'

export function AppCatalog({ orgId, projectId }: { orgId: string; projectId: string }) {
  const query = useAppDefinitions(orgId, projectId)
  if (query.isPending) return <Spinner className="size-4" />
  if (query.isError)
    return (
      <div role="alert">
        Could not load apps. <Button onClick={() => void query.refetch()}>Retry</Button>
      </div>
    )
  return (
    <div className="grid gap-4 sm:grid-cols-3">
      {query.data.data.map((definition) => {
        const label = appCatalog.find((app) => app.appType === definition.app_type)
        return (
          <Link
            key={definition.app_type}
            to="/projects/$projectId/apps/new/$appType"
            params={{ projectId, appType: definition.app_type }}
            className="hover:bg-muted/40 focus-visible:ring-ring flex flex-col gap-3 rounded-lg border p-5 transition-colors focus-visible:outline-none focus-visible:ring-2"
          >
            <div className="flex items-center justify-between gap-3">
              <div className="flex items-center gap-3">
                <AppIcon appType={definition.app_type} />
                <h2 className="font-medium">{label?.name ?? definition.app_type}</h2>
              </div>
              <ArrowRight className="text-muted-foreground size-4" aria-hidden="true" />
            </div>
            <p className="text-muted-foreground text-sm leading-relaxed">{label?.description}</p>
            <span className="mt-auto pt-2 text-sm font-medium">
              Set up {label?.name ?? definition.app_type}
            </span>
          </Link>
        )
      })}
    </div>
  )
}
