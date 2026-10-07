import type { Integration } from '@omnara/sdk'

import { IntegrationForm } from './IntegrationForm'
import { IntegrationSection } from './IntegrationSection'

export function IntegrationLaunch({
  orgId,
  projectId,
  integration,
  canEdit,
  initialSetup,
  onSetupFinished,
}: {
  orgId: string
  projectId: string
  integration: Integration
  canEdit: boolean
  initialSetup: boolean
  onSetupFinished: () => void
}) {
  const chat = integration.integration_kind !== 'github_pr'
  return (
    <IntegrationSection title={chat ? 'Mentions' : 'Pull requests'}>
      <IntegrationForm
        orgId={orgId}
        projectId={projectId}
        integrationKind={integration.integration_kind}
        integration={integration}
        canEdit={canEdit}
        defaultLauncherEnabled={initialSetup && !chat}
        onSaved={onSetupFinished}
        onDiscard={onSetupFinished}
      />
    </IntegrationSection>
  )
}
