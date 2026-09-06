import {
  type ProjectAvailableSecretListFilters,
  useProjectAvailableSecret,
  useProjectAvailableSecrets,
} from '@omnara/react'
import type { Secret } from '@omnara/sdk'
import type { ReactNode } from 'react'

import type { BasicMcpServer } from '@/components/agents/useAgentBuilderForm'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'

const SecretCombobox = createResourceCombobox<Secret>({
  itemKey: (secret) => secret.id,
  itemLabel: (secret) => secret.name,
  placeholder: 'Select secret',
})

export function AgentConfigMcpSecretCombobox({
  id,
  required,
  orgId,
  projectId,
  server,
  onChange,
  actions = [],
  knownSecret,
}: {
  id?: string
  required?: boolean
  orgId: string
  projectId: string
  server: BasicMcpServer
  onChange: (secretId: string) => void
  actions?: {
    label: string
    icon: ReactNode
    onSelect: () => void
    disabled?: boolean
    warning?: string
  }[]
  knownSecret?: Secret
}) {
  const search = useTypeaheadSearch()
  const oauth = server.authType === 'oauth'
  const secretKind = oauth
    ? 'oauth_token_set'
    : server.authType === 'sigv4'
      ? 'aws_credentials'
      : 'generic'
  const mcpUrl = server.url.trim()
  const matchesServer = (secret: Secret) =>
    secret.kind === secretKind && (!oauth || secret.metadata.mcp_url === mcpUrl)
  const filters: ProjectAvailableSecretListFilters = { ...search.filters, kind: secretKind }
  if (oauth) filters.metadata = { mcp_url: mcpUrl }
  const secretsQuery = useProjectAvailableSecrets(orgId, projectId, {
    filters,
    sort: 'name',
    pageSize: 25,
  })
  const listedSecrets = useInfiniteQueryItems(secretsQuery).map((access) => access.secret)
  const secrets =
    knownSecret &&
    matchesServer(knownSecret) &&
    !listedSecrets.some((secret) => secret.id === knownSecret.id)
      ? [knownSecret, ...listedSecrets]
      : listedSecrets
  const selectedSecretQuery = useProjectAvailableSecret(orgId, projectId, server.secretId)
  const resolvedSecret = selectedSecretQuery.data?.secret
  const selectedSecret =
    secrets.find((secret) => secret.id === server.secretId) ??
    (resolvedSecret?.id === server.secretId && matchesServer(resolvedSecret)
      ? resolvedSecret
      : null)

  return (
    <SecretCombobox
      id={id}
      required={required}
      items={secrets}
      value={selectedSecret}
      onValueChange={(secret) => {
        onChange(secret?.id ?? '')
      }}
      search={search}
      query={secretsQuery}
      placeholder={secretsQuery.isPending ? 'Loading secrets…' : 'Select secret'}
      emptyMessage={
        oauth
          ? 'No OAuth secrets match this server URL.'
          : server.authType === 'sigv4'
            ? 'No AWS credentials in this project.'
            : 'No generic secrets in this project.'
      }
      action={
        actions.length > 0 && (
          <>
            {actions.map((action) => (
              <button
                key={action.label}
                type="button"
                disabled={action.disabled}
                className="hover:bg-accent hover:text-accent-foreground flex w-full cursor-default items-center gap-2 rounded-sm py-2 pl-2 pr-8 text-sm outline-none disabled:opacity-50"
                onClick={action.onSelect}
              >
                {action.icon}
                <span className="flex min-w-0 flex-col items-start">
                  <span className="truncate">{action.label}</span>
                  {action.warning && (
                    <span className="truncate text-xs text-amber-600 dark:text-amber-500">
                      {action.warning}
                    </span>
                  )}
                </span>
              </button>
            ))}
          </>
        )
      }
    />
  )
}
