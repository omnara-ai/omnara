import { useProjectModelGrants } from '@omnara/react'
import type { ConfiguredModelSummary } from '@omnara/sdk'
import { useEffect, useState } from 'react'

import { PlusIcon } from '@/components/icons'
import { GrantProjectModelDialog } from '@/components/projects/GrantProjectModelDialog'
import { Field, RequiredFieldLabel } from '@/components/ui/field'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { exactNameGlob, useTypeaheadSearch } from '@/hooks/use-resource-list'
import { useProjectPage } from '@/lib/use-project-page'

type ModelOption = { kind: 'grant' } | { kind: 'model'; model: ConfiguredModelSummary }

const grantModelsOption: ModelOption = { kind: 'grant' }

const ModelCombobox = createResourceCombobox<ModelOption>({
  itemKey: (option) => (option.kind === 'grant' ? 'grant-models' : option.model.id),
  itemLabel: (option) =>
    option.kind === 'grant'
      ? 'Grant models…'
      : `${option.model.name} · ${option.model.provider_config}`,
  renderItem: (option) =>
    option.kind === 'grant' ? (
      <>
        <PlusIcon className="size-4" />
        <span>Grant models…</span>
      </>
    ) : (
      <span className="flex min-w-0 items-baseline gap-1.5">
        <span className="truncate">{option.model.name}</span>
        <span className="text-muted-foreground truncate text-xs">
          {option.model.provider_config}
        </span>
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
  onUnavailableChange?: (unavailable: boolean) => void
}) {
  const { project } = useProjectPage()
  const [grantOpen, setGrantOpen] = useState(false)
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
  const modelOptions: ModelOption[] = displayedModels.map((model) => ({ kind: 'model', model }))
  const selectedOption: ModelOption | null = selected ? { kind: 'model', model: selected } : null
  const options = project?.access.can_manage_access
    ? [grantModelsOption, ...modelOptions]
    : modelOptions
  const unavailable = lookupEnabled && completeSelection.isComplete && selected === null
  useEffect(() => {
    onUnavailableChange?.(unavailable)
  }, [onUnavailableChange, unavailable])

  return (
    <>
      <Field>
        <RequiredFieldLabel htmlFor="agent-config-model">Model</RequiredFieldLabel>
        <ModelCombobox
          id="agent-config-model"
          required
          items={options}
          value={selectedOption}
          onValueChange={(option) => {
            if (option?.kind === 'grant') {
              setGrantOpen(true)
              return
            }
            if (!option) return
            onChange({
              providerConfig: option.model.provider_config,
              modelName: option.model.name,
            })
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
        <ResourceNameFieldError value={value.providerConfig} fieldLabel="Provider name" />
        <ResourceNameFieldError value={value.modelName} fieldLabel="Model name" />
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
      <GrantProjectModelDialog
        open={grantOpen}
        onOpenChange={setGrantOpen}
        orgId={orgId}
        projectId={projectId}
      />
    </>
  )
}
