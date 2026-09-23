import type { AppType, ProjectApp } from '@omnara/sdk'
import type { ReactNode } from 'react'

import { ConnectGitHubForm } from './ConnectGitHubForm'
import { ConnectSlackForm } from './ConnectSlackForm'
import { ProjectAppSetup } from './ProjectAppSetup'

export function ProjectAppConnection({
  orgId,
  projectId,
  appType,
  app,
  onConnected,
  onCancel,
  footerAction,
  disabled,
}: {
  orgId: string
  projectId: string
  appType: AppType
  app?: ProjectApp
  onConnected: (app: ProjectApp) => void
  onCancel?: () => void
  footerAction?: ReactNode
  disabled?: boolean
}) {
  return (
    <section aria-label="Connection" className="flex flex-col gap-4 text-sm">
      <fieldset disabled={disabled} className="min-w-0">
        {appType === 'slack_thread' ? (
          <ConnectSlackForm
            orgId={orgId}
            projectId={projectId}
            app={app}
            onConnected={onConnected}
            onCancel={onCancel}
            footerAction={footerAction}
          />
        ) : appType === 'github_pr' ? (
          <ConnectGitHubForm
            orgId={orgId}
            projectId={projectId}
            app={app}
            onConnected={onConnected}
            onCancel={onCancel}
            footerAction={footerAction}
          />
        ) : (
          <ProjectAppSetup
            orgId={orgId}
            projectId={projectId}
            app={app}
            appType={appType}
            onSaved={onConnected}
            onCancel={onCancel}
            footerAction={footerAction}
          />
        )}
      </fieldset>
    </section>
  )
}
