import type { ProjectApp } from '@omnara/sdk'
import { useNavigate } from '@tanstack/react-router'
import { type ReactNode, useCallback, useState } from 'react'

import { RemoveProjectAppButton } from './ProjectAppActions'
import { ProjectAppConnection } from './ProjectAppConnection'
import { ProjectAppConnectionStatus } from './ProjectAppConnectionStatus'
import { ProjectAppPortalSetup } from './ProjectAppPortalSetup'
import { hasGitHubSetupReturn } from './useGitHubSetupReturn'
import { useProjectAppActions } from './useProjectAppActions'
import type { SlackOAuthOutcome } from './useSlackOAuthOutcome'

export function ProjectAppDetailLayout({
  orgId,
  projectId,
  app,
  canManage,
  canSetUp,
  draft,
  connected,
  onConnected,
  refreshFailed,
  onRefresh,
  oauth,
  children,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
  canManage: boolean
  canSetUp: boolean
  draft: boolean
  connected: boolean
  onConnected: (app: ProjectApp) => void
  refreshFailed: boolean
  onRefresh: () => void
  oauth: SlackOAuthOutcome | null
  children: ReactNode
}) {
  const navigate = useNavigate()
  const actions = useProjectAppActions(orgId, projectId)
  const [connecting, setConnecting] = useState(
    () => canSetUp && (draft || (app.app_type === 'github_pr' && hasGitHubSetupReturn())),
  )
  // A different flow activating the app must not unmount this connection attempt.
  const finishConnection = useCallback(
    (savedApp: ProjectApp) => {
      setConnecting(false)
      onConnected(savedApp)
    },
    [onConnected],
  )
  const showConnection = canSetUp && connecting
  const justConnected = connected && app.state === 'active' && !showConnection
  const removeAction = canManage ? (
    <RemoveProjectAppButton
      actions={actions}
      app={app}
      onRemoved={() => void navigate({ to: '/projects/$projectId/apps', params: { projectId } })}
    />
  ) : null
  return (
    <div className="flex w-full max-w-2xl flex-col gap-10">
      <ProjectAppConnectionStatus
        app={app}
        actions={actions}
        canManage={canManage}
        canSetUp={canSetUp}
        draft={draft}
        connecting={connecting}
        justConnected={justConnected}
        onReconnect={() => {
          setConnecting(true)
        }}
        refreshFailed={refreshFailed}
        onRefresh={onRefresh}
        oauth={oauth}
      />
      {showConnection && (
        <ProjectAppConnection
          footerAction={removeAction}
          disabled={actions.busy}
          orgId={orgId}
          projectId={projectId}
          appType={app.app_type}
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
      {justConnected && app.app_type === 'discord_thread' && (
        <ProjectAppPortalSetup appType="discord_thread" providerId={app.provider_tenant_id} />
      )}
      {children}
      {!showConnection && removeAction}
    </div>
  )
}
