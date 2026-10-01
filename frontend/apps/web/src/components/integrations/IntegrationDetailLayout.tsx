import type { Integration } from '@omnara/sdk'
import { useNavigate } from '@tanstack/react-router'
import { type ReactNode, useCallback, useState } from 'react'

import { RemoveIntegrationButton } from './IntegrationActions'
import { IntegrationConnection } from './IntegrationConnection'
import { IntegrationConnectionStatus } from './IntegrationConnectionStatus'
import { IntegrationPortalSetup } from './IntegrationPortalSetup'
import { hasGitHubSetupReturn } from './useGitHubSetupReturn'
import { useIntegrationActions } from './useIntegrationActions'
import type { SlackOAuthOutcome } from './useSlackOAuthOutcome'

export function IntegrationDetailLayout({
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
  integration: Integration
  canManage: boolean
  canSetUp: boolean
  draft: boolean
  connected: boolean
  onConnected: (integration: Integration) => void
  refreshFailed: boolean
  onRefresh: () => void
  oauth: SlackOAuthOutcome | null
  children: ReactNode
}) {
  const navigate = useNavigate()
  const actions = useIntegrationActions(orgId, projectId)
  const [connecting, setConnecting] = useState(
    () =>
      canSetUp &&
      (draft || (integration.integration_kind === 'github_pr' && hasGitHubSetupReturn())),
  )
  const finishConnection = useCallback(
    (savedIntegration: Integration) => {
      setConnecting(false)
      onConnected(savedIntegration)
    },
    [onConnected],
  )
  const showConnection = canSetUp && connecting
  const justConnected = connected && integration.state === 'active' && !showConnection
  const removeAction = canManage ? (
    <RemoveIntegrationButton
      actions={actions}
      integration={integration}
      onRemoved={() =>
        void navigate({ to: '/projects/$projectId/integrations', params: { projectId } })
      }
    />
  ) : null
  return (
    <div className="flex w-full max-w-2xl flex-col gap-10">
      <IntegrationConnectionStatus
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
        <IntegrationConnection
          footerAction={removeAction}
          disabled={actions.busy}
          orgId={orgId}
          projectId={projectId}
          integrationKind={integration.integration_kind}
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
      {justConnected && integration.integration_kind === 'discord_thread' && (
        <IntegrationPortalSetup
          integrationKind="discord_thread"
          providerId={integration.provider_tenant_id}
        />
      )}
      {children}
      {!showConnection && removeAction}
    </div>
  )
}
