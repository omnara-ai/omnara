import { useProjectApp } from '@omnara/react'
import { ApiError } from '@omnara/sdk'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'

import { appDefinitionLabel, appProvider } from '@/components/apps/appDefinitions'
import { ProjectAppActions } from '@/components/apps/ProjectAppActions'
import { ProjectAppForm } from '@/components/apps/ProjectAppForm'
import { ProjectAppSummary } from '@/components/apps/ProjectAppSummary'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'

export function ProjectAppPage() {
  const { appId = '' } = useParams({ strict: false })
  return (
    <ProjectPageFrame title="App settings">
      {({ activeOrg, projectId, project }) => (
        <ProjectAppDetail
          key={`${projectId}:${appId}`}
          orgId={activeOrg.id}
          projectId={projectId}
          appId={appId}
          canManage={project?.access.can_manage ?? false}
        />
      )}
    </ProjectPageFrame>
  )
}

export function ProjectAppDetail({
  orgId,
  projectId,
  appId,
  canManage,
}: {
  orgId: string
  projectId: string
  appId: string
  canManage: boolean
}) {
  const query = useProjectApp(orgId, projectId, appId)
  const [editing, setEditing] = useState(false)
  const navigate = useNavigate()
  if (query.isPending) return <Spinner className="size-4" />
  const unavailable =
    query.error instanceof ApiError && [401, 403, 404].includes(query.error.status)
  if (!query.data || unavailable)
    return (
      <div role="alert" className="flex flex-col gap-3">
        <p>Could not load this app. It may have been removed, or you may not have access.</p>
        <Button className="self-start" variant="outline" onClick={() => void query.refetch()}>
          Retry
        </Button>
        <Link to="/projects/$projectId/apps" params={{ projectId }}>
          Back to apps
        </Link>
      </div>
    )
  const app = query.data
  const provider = appProvider(app.settings.resource.definition)
  return (
    <div className="flex max-w-2xl flex-col gap-6">
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
          <Badge variant="outline">{appDefinitionLabel(app.settings.resource.definition)}</Badge>
          <Badge variant={app.enabled ? 'outline' : 'secondary'}>
            {app.enabled ? 'Enabled' : 'Disabled'}
          </Badge>
        </div>
        {canManage && !editing && (
          <ProjectAppActions
            orgId={orgId}
            projectId={projectId}
            app={app}
            onEdit={
              provider
                ? () => {
                    setEditing(true)
                  }
                : undefined
            }
            onRemoved={() =>
              void navigate({ to: '/projects/$projectId/apps', params: { projectId } })
            }
          />
        )}
      </header>
      {query.isError && (
        <div role="alert" className="flex flex-wrap items-center gap-3 text-sm">
          Could not refresh this app. Your current edits are kept.
          <Button size="sm" variant="outline" onClick={() => void query.refetch()}>
            Retry refresh
          </Button>
        </div>
      )}
      {editing && provider && canManage ? (
        <>
          <p className="text-muted-foreground text-sm">
            Changes apply to future launches and configurations. Existing agents keep their current
            capabilities.
          </p>
          <ProjectAppForm
            orgId={orgId}
            projectId={projectId}
            provider={provider}
            app={app}
            onSaved={() => {
              setEditing(false)
            }}
            onCancel={() => {
              setEditing(false)
            }}
          />
        </>
      ) : (
        <ProjectAppSummary orgId={orgId} projectId={projectId} app={app} />
      )}
    </div>
  )
}
