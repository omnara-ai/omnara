import type { ProjectApp } from '@omnara/sdk'

import { Button } from '@/components/ui/button'

import { ProjectAppForm } from './ProjectAppForm'
import { ProjectAppSection } from './ProjectAppSection'
import { ProjectAppSummary } from './ProjectAppSummary'

/** The event that starts agents: a readable summary that becomes its own editor in place. */
export function ProjectAppLaunch({
  orgId,
  projectId,
  app,
  canEdit,
  editing,
  onEditingChange,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
  canEdit: boolean
  editing: boolean
  onEditingChange: (editing: boolean) => void
}) {
  const chat = app.app_type !== 'github_pr'
  return (
    <ProjectAppSection
      title={chat ? 'Mentions' : 'Pull requests'}
      action={
        canEdit &&
        !editing && (
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              onEditingChange(true)
            }}
          >
            {app.settings.launcher ? 'Edit' : chat ? 'Choose profiles' : 'Choose a profile'}
          </Button>
        )
      }
    >
      {canEdit && editing ? (
        <ProjectAppForm
          orgId={orgId}
          projectId={projectId}
          appType={app.app_type}
          app={app}
          onSaved={() => {
            onEditingChange(false)
          }}
          onCancel={() => {
            onEditingChange(false)
          }}
        />
      ) : (
        <ProjectAppSummary orgId={orgId} projectId={projectId} app={app} />
      )}
    </ProjectAppSection>
  )
}
