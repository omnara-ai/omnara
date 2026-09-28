import type { Integration } from '@omnara/sdk'

import { IntegrationActions } from './IntegrationActions'
import { integrationKindLabel } from './integrationDefinitions'
import { IntegrationIcon } from './IntegrationIcon'
import type { useIntegrationActions } from './useIntegrationActions'

export function IntegrationHeader({
  actions,
  integration,
  onReconnect,
}: {
  actions?: ReturnType<typeof useIntegrationActions>
  integration: Integration
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
          integrationKind={integration.integration_kind}
          className="size-8 shrink-0"
        />
        <div className="min-w-0">
          <h1 className="type-title break-words">{integration.name}</h1>
          <p className="text-muted-foreground text-sm">
            {integrationKindLabel(integration.integration_kind)} · {status}
          </p>
        </div>
      </div>
      {actions && (onReconnect !== undefined || integration.state === 'active') && (
        <IntegrationActions
          actions={actions}
          integration={integration}
          onConnect={onReconnect}
        />
      )}
    </header>
  )
}
