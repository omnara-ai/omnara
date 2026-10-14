import { useIntegrations } from '@omnara/react'
import type { Integration } from '@omnara/sdk'

import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

type IntegrationOption = Pick<Integration, 'name'> & Partial<Pick<Integration, 'state'>>

const IntegrationCombobox = createResourceCombobox<IntegrationOption>({
  itemKey: (integration) => integration.name,
  itemLabel: (integration) => integration.name,
  renderItem: integrationLabel,
  renderValue: integrationLabel,
  placeholder: 'Search GitHub integrations…',
  emptyMessage: 'No GitHub integrations found.',
})

export function AgentConfigGitCredentialsField({
  orgId,
  projectId,
  integration,
  onIntegrationChange,
}: {
  orgId: string
  projectId: string
  integration: string
  onIntegrationChange: (integration: string) => void
}) {
  const query = useIntegrations(orgId, projectId, { pageSize: 100 })
  const items = useInfiniteQueryItems(query).filter((item) => item.integration_kind === 'github_pr')
  if (integration === '' && items.length === 0 && !query.isError && !query.hasNextPage) {
    return null
  }
  return (
    <FieldGroup className="bg-card rounded-xl border px-4 py-4 sm:px-5">
      <Field>
        <FieldLabel htmlFor="agent-config-git-credentials">Git credentials</FieldLabel>
        <div className="mt-2">
          <IntegrationCombobox
            id="agent-config-git-credentials"
            items={items}
            query={query}
            value={
              integration === ''
                ? null
                : (items.find((item) => item.name === integration) ?? { name: integration })
            }
            placeholder="Select a GitHub integration…"
            onValueChange={(item) => {
              onIntegrationChange(item?.name ?? '')
            }}
          />
        </div>
        <FieldDescription>
          Authenticate git on this agent’s machines with a GitHub integration.
        </FieldDescription>
      </Field>
    </FieldGroup>
  )
}

function integrationLabel(integration: IntegrationOption) {
  return (
    <span className="flex min-w-0 items-center gap-2">
      <span className="truncate">{integration.name}</span>
      {integration.state === 'disconnected' && (
        <span className="text-muted-foreground shrink-0 text-xs">Disconnected</span>
      )}
    </span>
  )
}
