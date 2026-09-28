import type { Integration } from '@omnara/sdk'

import { Button } from '@/components/ui/button'

import { IntegrationForm } from './IntegrationForm'
import { IntegrationSection } from './IntegrationSection'
import { IntegrationSummary } from './IntegrationSummary'

export function IntegrationLaunch({
  orgId,
  projectId,
  integration,
  canEdit,
  editing,
  onEditingChange,
}: {
  orgId: string
  projectId: string
  integration: Integration
  canEdit: boolean
  editing: boolean
  onEditingChange: (editing: boolean) => void
}) {
  const chat = integration.integration_kind !== 'github_pr'
  return (
    <IntegrationSection
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
        <IntegrationForm
          orgId={orgId}
          projectId={projectId}
          integrationKind={integration.integration_kind}
          integration={integration}
          onSaved={() => {
            onEditingChange(false)
          }}
          onCancel={() => {
            onEditingChange(false)
          }}
        />
      ) : (
        <IntegrationSummary orgId={orgId} projectId={projectId} integration={integration} />
      )}
    </IntegrationSection>
  )
}
