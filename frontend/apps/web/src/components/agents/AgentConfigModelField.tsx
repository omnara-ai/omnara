import { useProjectModelGrants } from '@omnara/react'
import type { ConfiguredModelSummary } from '@omnara/sdk'

import { GrantModelButton } from '@/components/projects/GrantModelButton'
import { Field, FieldLabel } from '@/components/ui/field'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { exactNameGlob, useTypeaheadSearch } from '@/hooks/use-resource-list'

const ModelCombobox = createResourceCombobox<ConfiguredModelSummary>({
  itemKey: (model) => model.id,
  itemLabel: (model) => model.name,
  renderItem: (model) => (
    <span className="flex min-w-0 flex-col">
      <span className="truncate">{model.name}</span>
      <span className="text-muted-foreground truncate text-xs">{model.provider_config}</span>
    </span>
  ),
  placeholder: 'Search granted models…',
  emptyMessage: 'No granted models found.',
})

export interface ModelSelection {
  providerConfig: string
  modelName: string
}

export function AgentConfigModelField({
  orgId,
  projectId,
  value,
  onChange,
}: {
  orgId: string
  projectId: string
  value: ModelSelection
  onChange: (selection: ModelSelection) => void
}) {
  const search = useTypeaheadSearch()
  const grantsQuery = useProjectModelGrants(orgId, projectId, {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
  })
  const models = useInfiniteQueryItems(grantsQuery).map((item) => item.model)
  const matchesValue = (model: ConfiguredModelSummary) =>
    model.name === value.modelName && model.provider_config === value.providerConfig
  const listedSelected = models.find(matchesValue)
  const lookupEnabled = value.modelName !== '' && value.providerConfig !== '' && !listedSelected
  const selectedQuery = useProjectModelGrants(orgId, projectId, {
    filters: { name: exactNameGlob(value.modelName) },
    pageSize: 25,
    enabled: lookupEnabled,
  })
  const completeSelection = useCompleteInfiniteQueryItems(selectedQuery, lookupEnabled)
  const selected =
    listedSelected ?? completeSelection.items.map((item) => item.model).find(matchesValue) ?? null
  const displayedModels =
    selected && !models.some((model) => model.id === selected.id) ? [selected, ...models] : models

  return (
    <Field>
      <div className="flex items-center justify-between gap-3">
        <FieldLabel>Model</FieldLabel>
        <GrantModelButton />
      </div>
      <p className="text-muted-foreground text-sm">
        Choose the exact granted model this agent will use.
      </p>
      <ModelCombobox
        items={displayedModels}
        value={selected}
        onValueChange={(model) => {
          if (!model) return
          onChange({ providerConfig: model.provider_config, modelName: model.name })
        }}
        search={search}
        query={grantsQuery}
        placeholder={
          grantsQuery.isPending
            ? 'Loading models…'
            : models.length === 0 && search.search === ''
              ? 'No models granted'
              : 'Search granted models…'
        }
        disabled={grantsQuery.isError || selectedQuery.isError}
      />
      {selected && <p className="text-muted-foreground text-xs">{selected.provider_config}</p>}
      <ResourceNameFieldError value={value.providerConfig} fieldLabel="Provider name" />
      <ResourceNameFieldError value={value.modelName} fieldLabel="Model name" />
      {grantsQuery.isError && (
        <p className="text-destructive text-sm">
          Could not load granted models.{' '}
          <button
            type="button"
            className="underline"
            onClick={() => {
              void grantsQuery.refetch()
            }}
          >
            Retry
          </button>
        </p>
      )}
      {selectedQuery.isError && (
        <p className="text-destructive text-sm">
          Could not load the selected model.{' '}
          <button
            type="button"
            className="underline"
            onClick={() => {
              void selectedQuery.refetch()
            }}
          >
            Retry
          </button>
        </p>
      )}
    </Field>
  )
}
