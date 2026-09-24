import type { IntegrationType, ProjectIntegration } from '@omnara/sdk'
import type { ReactNode } from 'react'

import { ConnectGitHubForm } from './ConnectGitHubForm'
import { ConnectSlackForm } from './ConnectSlackForm'
import { ProjectIntegrationSetup } from './ProjectIntegrationSetup'

export function ProjectIntegrationConnection({
  orgId,
  projectId,
  integrationType,
  integration,
  onConnected,
  onCancel,
  footerAction,
  disabled,
}: {
  orgId: string
  projectId: string
  integrationType: IntegrationType
  integration?: ProjectIntegration
  onConnected: (integration: ProjectIntegration) => void
  onCancel?: () => void
  footerAction?: ReactNode
  disabled?: boolean
}) {
  return (
    <section aria-label="Connection" className="flex flex-col gap-4 text-sm">
      <fieldset disabled={disabled} className="min-w-0">
        {integrationType === 'slack_thread' ? (
          <ConnectSlackForm
            orgId={orgId}
            projectId={projectId}
            integration={integration}
            onConnected={onConnected}
            onCancel={onCancel}
            footerAction={footerAction}
          />
        ) : integrationType === 'github_pr' ? (
          <ConnectGitHubForm
            orgId={orgId}
            projectId={projectId}
            integration={integration}
            onConnected={onConnected}
            onCancel={onCancel}
            footerAction={footerAction}
          />
        ) : (
          <ProjectIntegrationSetup
            orgId={orgId}
            projectId={projectId}
            integration={integration}
            integrationType={integrationType}
            onSaved={onConnected}
            onCancel={onCancel}
            footerAction={footerAction}
          />
        )}
      </fieldset>
    </section>
  )
}
