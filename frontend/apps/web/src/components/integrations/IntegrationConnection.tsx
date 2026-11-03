import type { Integration, IntegrationKind } from '@omnara/sdk'
import type { ReactNode } from 'react'

import { ConnectGitHubForm } from './ConnectGitHubForm'
import { ConnectSlackForm } from './ConnectSlackForm'
import { IntegrationSetup } from './IntegrationSetup'

export function IntegrationConnection({
  orgId,
  projectId,
  integrationKind,
  integration,
  slackOAuthFailed,
  onConnected,
  onCancel,
  footerAction,
  disabled,
}: {
  orgId: string
  projectId: string
  integrationKind: IntegrationKind
  integration?: Integration
  slackOAuthFailed?: boolean
  onConnected: (integration: Integration) => void
  onCancel?: () => void
  footerAction?: ReactNode
  disabled?: boolean
}) {
  return (
    <section aria-label="Connection" className="flex flex-col gap-4 text-sm">
      <fieldset disabled={disabled} className="min-w-0">
        {integrationKind === 'slack_thread' ? (
          <ConnectSlackForm
            orgId={orgId}
            projectId={projectId}
            integration={integration}
            defaultExistingApp={slackOAuthFailed}
            onCancel={onCancel}
            footerAction={footerAction}
          />
        ) : integrationKind === 'github_pr' ? (
          <ConnectGitHubForm
            orgId={orgId}
            projectId={projectId}
            integration={integration}
            onConnected={onConnected}
            onCancel={onCancel}
            footerAction={footerAction}
            disabled={disabled}
          />
        ) : (
          <IntegrationSetup
            orgId={orgId}
            projectId={projectId}
            integration={integration}
            integrationKind={integrationKind}
            onSaved={onConnected}
            onCancel={onCancel}
            footerAction={footerAction}
            disabled={disabled}
          />
        )}
      </fieldset>
    </section>
  )
}
