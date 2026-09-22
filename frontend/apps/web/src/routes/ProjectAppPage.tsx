import { useProjectApp } from '@omnara/react'
import { ApiError, type ProjectApp } from '@omnara/sdk'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useCallback, useState } from 'react'

import { appCatalog } from '@/components/apps/appDefinitions'
import { RemoveProjectAppButton } from '@/components/apps/ProjectAppActions'
import { ProjectAppAdvanced } from '@/components/apps/ProjectAppAdvanced'
import { ProjectAppConnection } from '@/components/apps/ProjectAppConnection'
import { ProjectAppConversations } from '@/components/apps/ProjectAppConversations'
import { ProjectAppHeader } from '@/components/apps/ProjectAppHeader'
import { ProjectAppLaunch } from '@/components/apps/ProjectAppLaunch'
import { ProjectAppPortalSetup } from '@/components/apps/ProjectAppPortalSetup'
import { ProjectAppSchedules } from '@/components/apps/ProjectAppSchedules'
import { useProjectAppActions } from '@/components/apps/useProjectAppActions'
import {
  type SlackOAuthOutcome,
  useSlackOAuthOutcome,
} from '@/components/apps/useSlackOAuthOutcome'
import { CircleCheck } from '@/components/icons'
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
  const navigate = useNavigate()
  const actions = useProjectAppActions(orgId, projectId)
  const appType = app.app_type
  const chat = appType === 'slack_thread' || appType === 'discord_thread'
  const canSetUp = canManage && appCatalog.some((definition) => definition.appType === appType)
  // A never-connected app is still in setup: connecting it is the whole page.
  const draft = app.state === 'disconnected' && !app.provider_tenant_id
  const [connecting, setConnecting] = useState(() => {
    const params = new URLSearchParams(window.location.search)
    const githubReturn =
      appType === 'github_pr' &&
      ['github_setup', 'github_setup_error', 'credentials_secret_ref', 'installation_id'].some(
        (key) => params.has(key),
      )
    return canSetUp && (draft || githubReturn)
  })
  const [connected, setConnected] = useState(oauth?.kind === 'success')
  const [editing, setEditing] = useState(
    canSetUp && oauth?.kind === 'success' && !app.settings.launcher,
  )
  // Keep setup mounted until its own authorization flow finishes, even if another flow
  // activates the app first. The Slack form also refreshes the apps list before completing.
  const finishConnection = useCallback(
    (savedApp: ProjectApp) => {
      setConnecting(false)
      setConnected(true)
      if (canSetUp && !savedApp.settings.launcher) setEditing(true)
      onOAuthCleared()
    },
    [canSetUp, onOAuthCleared],
  )
  const showConnection = canSetUp && connecting
  const removeAction = canManage ? (
    <RemoveProjectAppButton
      actions={actions}
      app={app}
      onRemoved={() => void navigate({ to: '/projects/$projectId/apps', params: { projectId } })}
    />
  ) : null
  return (
    <div className="flex w-full max-w-2xl flex-col gap-10">
      <div className="flex flex-col gap-4">
        <ProjectAppHeader
          actions={canManage ? actions : undefined}
          app={app}
          onReconnect={
            canSetUp && app.state === 'active' && !connecting
              ? () => {
                  setConnecting(true)
                }
              : undefined
          }
        />
        {refreshFailed && (
          <div role="alert" className="flex flex-wrap items-center gap-3 text-sm">
            Could not refresh this app. Your current edits are kept.
            <Button size="sm" variant="outline" onClick={onRefresh}>
              Retry refresh
            </Button>
          </div>
        )}
        {oauth?.kind === 'error' && (
          <p role="alert" className="text-destructive text-sm">
            Slack setup didn’t finish. {oauth.description}
          </p>
        )}
        {connected && app.state === 'active' && !showConnection && (
          <ConnectedNotice app={app} chooseNext={canSetUp && !app.settings.launcher} />
        )}
        {draft && !canSetUp && (
          <p className="text-muted-foreground text-sm">
            Setup isn’t finished. Ask a project administrator to connect this app.
          </p>
        )}
        {!draft && app.state === 'disconnected' && (
          <div className="flex flex-col items-start gap-3 text-sm">
            <p className="text-muted-foreground">
              This app is disconnected. {chat ? 'Mentions, schedules' : 'Launches'} and conversation
              forwarding are paused; settings, agents and history are kept.
            </p>
            {canSetUp && !connecting && (
              <Button
                onClick={() => {
                  setConnecting(true)
                }}
              >
                Reconnect account
              </Button>
            )}
          </div>
        )}
      </div>
      {showConnection && (
        <ProjectAppConnection
          footerAction={removeAction}
          disabled={actions.busy}
          orgId={orgId}
          projectId={projectId}
          app={app}
          onConnected={finishConnection}
          onCancel={
            draft
              ? undefined
              : () => {
                  setConnecting(false)
                }
          }
        />
      )}
      {connected &&
        app.state === 'active' &&
        app.app_type === 'discord_thread' &&
        !showConnection && (
          <ProjectAppPortalSetup appType="discord_thread" providerId={app.provider_tenant_id} />
        )}
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
      {!showConnection && removeAction}
    </div>
  )
}

function ConnectedNotice({ app, chooseNext }: { app: ProjectApp; chooseNext: boolean }) {
  const chat = app.app_type !== 'github_pr'
  return (
    <p role="status" className="flex items-start gap-2 text-sm">
      <CircleCheck className="text-primary mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <span>
        Account connected.
        {app.app_type === 'discord_thread' && ' Discord setup steps are below if you need them.'}
        {chooseNext &&
          (chat
            ? ' Choose which agents people can start by mentioning the bot, or add a schedule instead.'
            : ' Choose an agent profile and when pull requests start agents.')}
      </span>
    </p>
  )
}
