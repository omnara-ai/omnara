import { useProjectModelGrants } from '@omnara/react'
import type { ConfiguredModelSummary } from '@omnara/sdk'
import { useEffect } from 'react'

import { GrantModelButton } from '@/components/projects/GrantModelButton'
import { Field, FieldLabel } from '@/components/ui/field'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { exactNameGlob, useTypeaheadSearch } from '@/hooks/use-resource-list'

const ModelCombobox = createResourceCombobox<ConfiguredModelSummary>({
  itemKey: (model) => model.id,
  itemLabel: (model) => `${model.name} · ${model.provider_config}`,
  renderItem: (model) => (
    <span className="flex min-w-0 items-baseline gap-1.5">
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
  onUnavailableChange,
}: {
  orgId: string
  projectId: string
  value: ModelSelection
  onChange: (selection: ModelSelection) => void
  /** Reports whether the selected model no longer resolves to a granted model,
   *  once the point lookup completes. The value is kept so an existing config
   *  stays editable; callers surface the problem. */
  onUnavailableChange?: (unavailable: boolean) => void
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
  const unavailable = lookupEnabled && completeSelection.isComplete && selected === null
  useEffect(() => {
    onUnavailableChange?.(unavailable)
  }, [onUnavailableChange, unavailable])

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
      {unavailable && (
        <p className="text-destructive text-sm">
          The configured model “{value.modelName}” ({value.providerConfig}) is no longer available
          to the project. Pick another model or grant it again.
        </p>
      )}
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
