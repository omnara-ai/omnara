import type { ProjectApp } from '@omnara/sdk'

import { Button } from '@/components/ui/button'

import { ConnectSlackForm } from './ConnectSlackForm'
import { ProjectAppSection } from './ProjectAppSection'
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
  const reconnect = Boolean(app.provider_tenant_id)
  if (app.app_type === 'slack_thread')
    return (
      <ProjectAppSection
        title={reconnect ? 'Reconnect Slack' : 'Connect Slack'}
        action={
          onCancel && (
            <Button size="sm" variant="ghost" onClick={onCancel}>
              Cancel
            </Button>
          )
        }
      >
        {!reconnect && (
          <p className="text-muted-foreground">
            Omnara can create the Slack app for you, or you can use one you already have. After
            Slack authorizes it, you’ll choose which agents people can start.
          </p>
        )}
        <ConnectSlackForm orgId={orgId} projectId={projectId} app={app} onConnected={onConnected} />
      </ProjectAppSection>
    )
  return (
    <section aria-label="Connection" className="flex flex-col gap-4 text-sm">
      <ProjectAppSetup
        orgId={orgId}
        projectId={projectId}
        app={app}
        appType={app.app_type}
        onSaved={onConnected}
        onCancel={onCancel}
      />
    </section>
  )
}
