import type { ProjectApp } from '@omnara/sdk'

import { appTypeLabel } from './appDefinitions'
import { AppIcon } from './AppIcon'
import { ProjectAppActions } from './ProjectAppActions'

/** Identity and account state live here, so the page body is only what people configure. */
export function ProjectAppHeader({
  orgId,
  projectId,
  app,
  canManage,
  onReconnect,
  onRemoved,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
  canManage: boolean
  onReconnect?: () => void
  onRemoved: () => void
}) {
  const status =
    app.state === 'active'
      ? app.provider_agent_display_name
        ? `Connected as ${app.provider_agent_display_name}`
        : 'Connected'
      : app.provider_tenant_id
        ? 'Disconnected'
        : 'Not connected yet'
  return (
    <header className="flex items-start gap-3">
      <div className="flex min-w-0 flex-1 items-center gap-3">
        <AppIcon appType={app.app_type} className="size-8 shrink-0" />
        <div className="min-w-0">
          <h1 className="type-title break-words">{app.name}</h1>
          <p className="text-muted-foreground text-sm">
            {appTypeLabel(app.app_type)} · {status}
          </p>
        </div>
      </div>
      {canManage && (
        <ProjectAppActions
          orgId={orgId}
          projectId={projectId}
          app={app}
          onConnect={onReconnect}
          onRemoved={onRemoved}
        />
      )}
    </header>
  )
}
