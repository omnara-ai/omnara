import { useAppDefinitions } from '@omnara/react'
import { Link } from '@tanstack/react-router'

import { ArrowRight } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'

import { appCatalog } from './appDefinitions'

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
        const label = appCatalog.find((app) => app.provider === definition.provider)
        return (
          <Link
            key={definition.id}
            to="/projects/$projectId/apps/new/$provider"
            params={{ projectId, provider: definition.provider }}
            className="hover:bg-muted/40 focus-visible:ring-ring flex flex-col gap-3 rounded-lg border p-5 transition-colors focus-visible:outline-none focus-visible:ring-2"
          >
            <div className="flex items-center justify-between gap-3">
              <h2 className="font-medium">{label?.name ?? definition.id}</h2>
              <ArrowRight className="text-muted-foreground size-4" aria-hidden="true" />
            </div>
            <p className="text-muted-foreground text-sm leading-relaxed">{label?.description}</p>
            <span className="mt-auto pt-2 text-sm font-medium">
              Set up {label?.name ?? definition.id}
            </span>
          </Link>
        )
      })}
    </div>
  )
}
