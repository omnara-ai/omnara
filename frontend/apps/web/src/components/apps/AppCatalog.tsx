import { Link } from '@tanstack/react-router'

import { ArrowRight } from '@/components/icons'

import { appCatalog } from './appDefinitions'

export function AppCatalog({ projectId }: { projectId: string }) {
  return (
    <div className="grid gap-4 sm:grid-cols-3">
      {appCatalog.map((app) => (
        <Link
          key={app.provider}
          to="/projects/$projectId/apps/new/$provider"
          params={{ projectId, provider: app.provider }}
          className="hover:bg-muted/40 focus-visible:ring-ring flex flex-col gap-3 rounded-lg border p-5 transition-colors focus-visible:outline-none focus-visible:ring-2"
        >
          <div className="flex items-center justify-between gap-3">
            <h2 className="font-medium">{app.name}</h2>
            <ArrowRight className="text-muted-foreground size-4" aria-hidden="true" />
          </div>
          <p className="text-muted-foreground text-sm leading-relaxed">{app.description}</p>
          <span className="mt-auto pt-2 text-sm font-medium">Set up {app.name}</span>
        </Link>
      ))}
    </div>
  )
}
