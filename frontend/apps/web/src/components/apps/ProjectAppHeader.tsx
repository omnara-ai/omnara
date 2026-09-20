import type { ProjectApp } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

import { appDefinitionLabel, appProvider } from './appDefinitions'
import { ProjectAppActions } from './ProjectAppActions'

export function ProjectAppHeader({
  orgId,
  projectId,
  app,
  canManage,
  viewing,
  onConnect,
  onEdit,
  onRemoved,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
  canManage: boolean
  viewing: boolean
  onConnect: () => void
  onEdit: () => void
  onRemoved: () => void
}) {
  const provider = appProvider(app.definition_id)
  return (
    <header className="flex flex-col gap-3">
      <Link
        className="text-muted-foreground text-sm hover:underline"
        to="/projects/$projectId/apps"
        params={{ projectId }}
      >
        Back to apps
      </Link>
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="type-title">{app.name}</h1>
        <Badge variant="outline">{appDefinitionLabel(app.definition_id)}</Badge>
        <Badge variant={app.state === 'active' ? 'outline' : 'secondary'}>
          {app.state === 'active' ? 'Connected' : 'Disconnected'}
        </Badge>
      </div>
      {app.state === 'disconnected' && !app.provider_tenant_id && viewing && (
        <p className="text-muted-foreground text-sm">
          {canManage
            ? 'Finish setup: connect an account to use this app’s capabilities, then choose optional launch settings.'
            : 'Setup is unfinished. Ask a project administrator to connect this app.'}
        </p>
      )}
      {canManage && viewing && provider && (
        <Button
          className="self-start"
          variant={app.state === 'active' ? 'outline' : 'default'}
          onClick={onConnect}
        >
          {app.provider_tenant_id ? 'Reconnect account' : 'Connect account'}
        </Button>
      )}
      {canManage && viewing && (
        <ProjectAppActions
          orgId={orgId}
          projectId={projectId}
          app={app}
          onEdit={provider ? onEdit : undefined}
          onRemoved={onRemoved}
        />
      )}
    </header>
  )
}
