import type { ProjectApp } from '@omnara/sdk'

import { ConnectSlackForm } from './ConnectSlackForm'
import { ProjectAppSetup } from './ProjectAppSetup'

/** Inline account connection. A never-connected app has nothing to cancel back to. */
export function ProjectAppConnection({
  orgId,
  projectId,
  app,
  onConnected,
  onCancel,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
  onConnected: (app: ProjectApp) => void
  onCancel?: () => void
}) {
  return (
    <section aria-label="Connection" className="flex flex-col gap-4 text-sm">
      {app.app_type === 'slack_thread' ? (
        <ConnectSlackForm
          orgId={orgId}
          projectId={projectId}
          app={app}
          onConnected={onConnected}
          onCancel={onCancel}
        />
      ) : (
        <ProjectAppSetup
          orgId={orgId}
          projectId={projectId}
          app={app}
          appType={app.app_type}
          onSaved={onConnected}
          onCancel={onCancel}
        />
      )}
    </section>
  )
}
