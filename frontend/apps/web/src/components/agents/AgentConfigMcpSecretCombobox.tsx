import { useProjectAvailableSecrets } from '@omnara/react'
import type { Secret } from '@omnara/sdk'

import type { BasicMcpServer } from '@/components/agents/agentConfigBasicSerialization'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'

const SecretCombobox = createResourceCombobox<Secret>({
  itemKey: (secret) => secret.id,
  itemLabel: (secret) => secret.name,
  placeholder: 'Select secret',
})

export function AgentConfigMcpSecretCombobox({
  orgId,
  projectId,
  server,
  onChange,
}: {
  orgId: string
  projectId: string
  server: BasicMcpServer
  onChange: (secretId: string) => void
}) {
  const search = useTypeaheadSearch()
  const oauth = server.authType === 'oauth'
  const secretKind = oauth
    ? 'oauth_token_set'
    : server.authType === 'sigv4'
      ? 'aws_credentials'
      : 'generic'
  const secretsQuery = useProjectAvailableSecrets(orgId, projectId, {
    filters: {
      ...search.filters,
      ...(oauth ? { metadata: { mcp_url: server.url.trim() } } : {}),
    },
    sort: 'name',
    pageSize: 25,
  })
  const secrets = useInfiniteQueryItems(secretsQuery)
    .map((access) => access.secret)
    .filter((secret) => secret.kind === secretKind)

  return (
    <SecretCombobox
      items={secrets}
      value={server.secretId || null}
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
    />
  )
}
