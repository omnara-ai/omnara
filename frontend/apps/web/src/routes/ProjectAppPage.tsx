import { useProjectApp } from '@omnara/react'
import { ApiError, type ProjectApp } from '@omnara/sdk'
import { Link, useParams } from '@tanstack/react-router'
import { useCallback, useState } from 'react'

import { appCatalog } from '@/components/apps/appDefinitions'
import { ProjectAppAdvanced } from '@/components/apps/ProjectAppAdvanced'
import { ProjectAppConversations } from '@/components/apps/ProjectAppConversations'
import { ProjectAppDetailLayout } from '@/components/apps/ProjectAppDetailLayout'
import { ProjectAppLaunch } from '@/components/apps/ProjectAppLaunch'
import { ProjectAppSchedules } from '@/components/apps/ProjectAppSchedules'
import {
  type SlackOAuthOutcome,
  useSlackOAuthOutcome,
} from '@/components/apps/useSlackOAuthOutcome'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'

export function ProjectAppPage() {
  const { projectId = '', appId = '' } = useParams({ strict: false })
  return (
    <ProjectPageFrame
      title="App"
      breadcrumbs={[
        { id: 'apps', label: 'Apps', to: '/projects/$projectId/apps', params: { projectId } },
      ]}
    >
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
  const { outcome: oauth, clear: clearOAuth } = useSlackOAuthOutcome(appId)
  const query = useProjectApp(orgId, projectId, appId)
  if (query.isPending) return <Spinner className="size-4" />
  const unavailable =
    query.error instanceof ApiError && [401, 403, 404].includes(query.error.status)
  if (!query.data || unavailable)
    return (
      <div role="alert" className="flex flex-col gap-3">
        <p>Could not load this app. It may have been removed, or you may not have access.</p>
        {oauth?.kind === 'error' && <p>{oauth.description}</p>}
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
      oauth={oauth}
      onOAuthCleared={clearOAuth}
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
  oauth,
  onOAuthCleared,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
  canManage: boolean
  refreshFailed: boolean
  onRefresh: () => void
  oauth: SlackOAuthOutcome | null
  onOAuthCleared: () => void
}) {
  const appType = app.app_type
  const canSetUp = canManage && appCatalog.some((definition) => definition.appType === appType)
  const draft = app.state === 'disconnected' && !app.provider_tenant_id
  const [connected, setConnected] = useState(oauth?.kind === 'success')
  const [editing, setEditing] = useState(
    canSetUp && oauth?.kind === 'success' && !app.settings.launcher,
  )
  const finishConnection = useCallback(
    (savedApp: ProjectApp) => {
      setConnected(true)
      if (canSetUp && !savedApp.settings.launcher) setEditing(true)
      onOAuthCleared()
    },
    [canSetUp, onOAuthCleared],
  )
  return (
    <ProjectAppDetailLayout
      orgId={orgId}
      projectId={projectId}
      app={app}
      canManage={canManage}
      canSetUp={canSetUp}
      draft={draft}
      connected={connected}
      onConnected={finishConnection}
      refreshFailed={refreshFailed}
      onRefresh={onRefresh}
      oauth={oauth}
    >
      {!draft && (
        <ProjectAppLaunch
          orgId={orgId}
          projectId={projectId}
          app={app}
          canEdit={canSetUp}
          editing={editing}
          onEditingChange={(next) => {
            setEditing(next)
            if (!next) setConnected(false)
          }}
        />
      )}
      {app.capabilities.schedule && (
        <ProjectAppSchedules
          orgId={orgId}
          projectId={projectId}
          app={app}
          canManage={canManage}
          hideWhenEmpty={draft}
        />
      )}
      <ProjectAppConversations
        orgId={orgId}
        projectId={projectId}
        app={app}
        canManage={canManage}
        hideWhenEmpty={draft}
      />
      {!draft && <ProjectAppAdvanced app={app} />}
    </ProjectAppDetailLayout>
  )
}
