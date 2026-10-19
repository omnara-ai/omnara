import { exactNameGlob, useIntegrationGitHubRepositories, useIntegrations } from '@omnara/react'
import type {
  AgentConfigDefinitionGitCredentials,
  GitHubRepository,
  Integration,
} from '@omnara/sdk'

import {
  CheckboxField,
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { createResourceMultiCombobox } from '@/components/ui/resource-multi-combobox'
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

const RepositoryCombobox = createResourceMultiCombobox<GitHubRepository>({
  itemKey: (repository) => String(repository.id),
  itemLabel: (repository) => repository.name,
  placeholder: 'All repositories',
  emptyMessage: 'No repositories found.',
})

export function AgentConfigGitCredentialsField({
  orgId,
  projectId,
  value,
  onChange,
}: {
  orgId: string
  projectId: string
  value: AgentConfigDefinitionGitCredentials | undefined
  onChange: (value: AgentConfigDefinitionGitCredentials | undefined) => void
}) {
  const { query, items, selected } = useGitHubIntegrations(orgId, projectId, value?.integration)
  const repositories = useIntegrationGitHubRepositories(orgId, projectId, selected?.id ?? '')
  const repositoryOptions = repositories.data?.repositories ?? []
  if (value == null && items.length === 0 && !query.isError && !query.hasNextPage) {
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
            value={value == null ? null : (selected ?? { name: value.integration })}
            placeholder="Select a GitHub integration…"
            onValueChange={(item) => {
              if (item?.name === value?.integration) return
              onChange(item == null ? undefined : gitCredentials(item.name, [], value?.read_only))
            }}
          />
        </div>
        <FieldDescription>
          Authenticate git on this agent’s machines with a GitHub integration.
        </FieldDescription>
      </Field>
      {value != null && (
        <>
          <Field>
            <FieldLabel htmlFor="agent-config-git-repositories">Repositories</FieldLabel>
            <RepositoryCombobox
              id="agent-config-git-repositories"
              items={repositoryOptions}
              pending={repositories.isLoading}
              placeholder={value.repository_ids ? 'Add repositories…' : undefined}
              emptyMessage={repositories.isError ? 'Couldn’t load repositories.' : undefined}
              value={(value.repository_ids ?? []).map(
                (id) =>
                  repositoryOptions.find((repository) => repository.id === id) ?? {
                    id,
                    name: String(id),
                  },
              )}
              onValueChange={(options) => {
                onChange(
                  gitCredentials(
                    value.integration,
                    options.map((option) => option.id),
                    value.read_only,
                  ),
                )
              }}
            />
          </Field>
          <CheckboxField
            className="items-start"
            inputClassName="mt-0.5"
            label="Read only"
            description="Agents can clone but not push, even if the integration allows it."
            checked={value.read_only === true}
            onChange={(event) => {
              onChange(
                gitCredentials(value.integration, value.repository_ids ?? [], event.target.checked),
              )
            }}
          />
        </>
      )}
    </FieldGroup>
  )
}

function useGitHubIntegrations(orgId: string, projectId: string, name: string | undefined) {
  const query = useIntegrations(orgId, projectId, { pageSize: 100 })
  const items = useInfiniteQueryItems(query).filter((item) => item.integration_kind === 'github_pr')
  const listed = items.find((item) => item.name === name)
  const lookup = useIntegrations(orgId, projectId, {
    filters: { name: exactNameGlob(name ?? '') },
    enabled: name != null && listed == null,
  })
  const lookedUp = useInfiniteQueryItems(lookup).find((item) => item.name === name)
  return { query, items, selected: listed ?? lookedUp }
}

function gitCredentials(
  integration: string,
  repositoryIds: number[],
  readOnly: boolean | undefined,
): AgentConfigDefinitionGitCredentials {
  const credentials: AgentConfigDefinitionGitCredentials = { integration }
  if (repositoryIds.length > 0) credentials.repository_ids = repositoryIds
  if (readOnly === true) credentials.read_only = true
  return credentials
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
