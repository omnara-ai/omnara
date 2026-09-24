import type { ProjectIntegration } from '@omnara/sdk'

import { Button } from '@/components/ui/button'

import { ProjectIntegrationForm } from './ProjectIntegrationForm'
import { ProjectIntegrationSection } from './ProjectIntegrationSection'
import { ProjectIntegrationSummary } from './ProjectIntegrationSummary'

export function ProjectIntegrationLaunch({
  orgId,
  projectId,
  integration,
  canEdit,
  editing,
  onEditingChange,
}: {
  orgId: string
  projectId: string
  integration: ProjectIntegration
  canEdit: boolean
  editing: boolean
  onEditingChange: (editing: boolean) => void
}) {
  const chat = integration.integration_type !== 'github_pr'
  return (
    <ProjectIntegrationSection
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
            {integration.settings.launcher ? 'Edit' : chat ? 'Choose profiles' : 'Choose a profile'}
          </Button>
        )
      }
    >
      {canEdit && editing ? (
        <ProjectIntegrationForm
          orgId={orgId}
          projectId={projectId}
          integrationType={integration.integration_type}
          integration={integration}
          onSaved={() => {
            onEditingChange(false)
          }}
          onCancel={() => {
            onEditingChange(false)
          }}
        />
      ) : (
        <ProjectIntegrationSummary orgId={orgId} projectId={projectId} integration={integration} />
      )}
    </ProjectIntegrationSection>
  )
}
