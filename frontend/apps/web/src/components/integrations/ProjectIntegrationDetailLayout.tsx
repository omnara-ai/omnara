import type { ProjectIntegration } from '@omnara/sdk'
import { useNavigate } from '@tanstack/react-router'
import { type ReactNode, useCallback, useState } from 'react'

import { RemoveProjectIntegrationButton } from './ProjectIntegrationActions'
import { ProjectIntegrationConnection } from './ProjectIntegrationConnection'
import { ProjectIntegrationConnectionStatus } from './ProjectIntegrationConnectionStatus'
import { ProjectIntegrationPortalSetup } from './ProjectIntegrationPortalSetup'
import { hasGitHubSetupReturn } from './useGitHubSetupReturn'
import { useProjectIntegrationActions } from './useProjectIntegrationActions'
import type { SlackOAuthOutcome } from './useSlackOAuthOutcome'

export function ProjectIntegrationDetailLayout({
  orgId,
  projectId,
  integration,
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
  integration: ProjectIntegration
  canManage: boolean
  canSetUp: boolean
  draft: boolean
  connected: boolean
  onConnected: (integration: ProjectIntegration) => void
  refreshFailed: boolean
  onRefresh: () => void
  oauth: SlackOAuthOutcome | null
  children: ReactNode
}) {
  const navigate = useNavigate()
  const actions = useProjectIntegrationActions(orgId, projectId)
  const [connecting, setConnecting] = useState(
    () =>
      canSetUp &&
      (draft || (integration.integration_type === 'github_pr' && hasGitHubSetupReturn())),
  )
  // A different flow activating the integration must not unmount this connection attempt.
  const finishConnection = useCallback(
    (savedIntegration: ProjectIntegration) => {
      setConnecting(false)
      onConnected(savedIntegration)
    },
    [onConnected],
  )
  const showConnection = canSetUp && connecting
  const justConnected = connected && integration.state === 'active' && !showConnection
  const removeAction = canManage ? (
    <RemoveProjectIntegrationButton
      actions={actions}
      integration={integration}
      onRemoved={() =>
        void navigate({ to: '/projects/$projectId/integrations', params: { projectId } })
      }
    />
  ) : null
  return (
    <div className="flex w-full max-w-2xl flex-col gap-10">
      <ProjectIntegrationConnectionStatus
        integration={integration}
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
        <ProjectIntegrationConnection
          footerAction={removeAction}
          disabled={actions.busy}
          orgId={orgId}
          projectId={projectId}
          integrationType={integration.integration_type}
          integration={integration}
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
      {justConnected && integration.integration_type === 'discord_thread' && (
        <ProjectIntegrationPortalSetup
          integrationType="discord_thread"
          providerId={integration.provider_tenant_id}
        />
      )}
      {children}
      {!showConnection && removeAction}
    </div>
  )
}
