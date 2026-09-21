import { useProjectApp } from '@omnara/react'
import { ApiError, type ProjectApp } from '@omnara/sdk'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'

import { appCatalog } from '@/components/apps/appDefinitions'
import { ConnectSlackDialog } from '@/components/apps/ConnectSlackDialog'
import { ProjectAppConversations } from '@/components/apps/ProjectAppConversations'
import { ProjectAppForm } from '@/components/apps/ProjectAppForm'
import { ProjectAppHeader } from '@/components/apps/ProjectAppHeader'
import { ProjectAppSchedules } from '@/components/apps/ProjectAppSchedules'
import { ProjectAppSetup } from '@/components/apps/ProjectAppSetup'
import { ProjectAppSummary } from '@/components/apps/ProjectAppSummary'
import { SlackOAuthOutcomeDialog } from '@/components/apps/SlackOAuthOutcomeDialog'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
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
  return (
    <ProjectAppSettings
      orgId={orgId}
      projectId={projectId}
      app={query.data}
      canManage={canManage}
      refreshFailed={query.isError}
      onRefresh={() => void query.refetch()}
    />
  )
}

function ProjectAppSettings({
  orgId,
  projectId,
  app,
  canManage,
  refreshFailed,
  onRefresh,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
  canManage: boolean
  refreshFailed: boolean
  onRefresh: () => void
}) {
  const [editing, setEditing] = useState(false)
  const [connecting, setConnecting] = useState(false)
  const navigate = useNavigate()
  const appType = app.app_type
  const supported = appCatalog.some((definition) => definition.appType === appType)
  const viewing = !editing && !connecting
  return (
    <div className="flex max-w-2xl flex-col gap-6">
      <ProjectAppHeader
        orgId={orgId}
        projectId={projectId}
        app={app}
        canManage={canManage}
        viewing={viewing}
        onConnect={() => {
          setConnecting(true)
        }}
        onEdit={() => {
          setEditing(true)
        }}
        onRemoved={() => void navigate({ to: '/projects/$projectId/apps', params: { projectId } })}
      />
      {refreshFailed && (
        <div role="alert" className="flex flex-wrap items-center gap-3 text-sm">
          Could not refresh this app. Your current edits are kept.
          <Button size="sm" variant="outline" onClick={onRefresh}>
            Retry refresh
          </Button>
        </div>
      )}
      {connecting &&
        supported &&
        canManage &&
        (appType === 'slack_thread' ? (
          <ConnectSlackDialog
            open
            app={app}
            orgId={orgId}
            projectId={projectId}
            onOpenChange={setConnecting}
            onConnected={() => {
              setEditing(true)
            }}
          />
        ) : (
          <ProjectAppSetup
            orgId={orgId}
            projectId={projectId}
            app={app}
            onSaved={() => {
              setConnecting(false)
              setEditing(true)
            }}
            onCancel={() => {
              setConnecting(false)
            }}
          />
        ))}
      {editing && supported && canManage ? (
        <>
          <p className="text-muted-foreground text-sm">
            Changes apply to future launches and configurations. Existing agents keep their current
            capabilities.
          </p>
          <ProjectAppForm
            orgId={orgId}
            projectId={projectId}
            appType={appType}
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
        !connecting && <ProjectAppSummary orgId={orgId} projectId={projectId} app={app} />
      )}
      {viewing && (
        <ProjectAppConversations
          orgId={orgId}
          projectId={projectId}
          app={app}
          canManage={canManage}
        />
      )}
      {viewing && (appType === 'slack_thread' || appType === 'discord_thread') && (
        <ProjectAppSchedules orgId={orgId} projectId={projectId} app={app} canManage={canManage} />
      )}
      <SlackOAuthOutcomeDialog />
    </div>
  )
}
