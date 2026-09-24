import type { ProjectIntegration } from '@omnara/sdk'

import { integrationTypeLabel } from './integrationDefinitions'
import { IntegrationIcon } from './IntegrationIcon'
import { ProjectIntegrationActions } from './ProjectIntegrationActions'
import type { useProjectIntegrationActions } from './useProjectIntegrationActions'

export function ProjectIntegrationHeader({
  actions,
  integration,
  onReconnect,
}: {
  actions?: ReturnType<typeof useProjectIntegrationActions>
  integration: ProjectIntegration
  onReconnect?: () => void
}) {
  const status =
    integration.state === 'active'
      ? integration.provider_agent_display_name
        ? `Connected as ${integration.provider_agent_display_name}`
        : 'Connected'
      : integration.provider_tenant_id
        ? 'Disconnected'
        : 'Not connected yet'
  return (
    <header className="flex items-start gap-3">
      <div className="flex min-w-0 flex-1 items-center gap-3">
        <IntegrationIcon
          integrationType={integration.integration_type}
          className="size-8 shrink-0"
        />
        <div className="min-w-0">
          <h1 className="type-title break-words">{integration.name}</h1>
          <p className="text-muted-foreground text-sm">
            {integrationTypeLabel(integration.integration_type)} · {status}
          </p>
        </div>
      </div>
      {actions && (onReconnect !== undefined || integration.state === 'active') && (
        <ProjectIntegrationActions
          actions={actions}
          integration={integration}
          onConnect={onReconnect}
        />
      )}
    </header>
  )
}
