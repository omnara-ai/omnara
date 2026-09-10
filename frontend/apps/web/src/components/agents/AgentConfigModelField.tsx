import { useProjectModelGrants } from '@omnara/react'
import type { ConfiguredModelSummary } from '@omnara/sdk'
import { useEffect, useRef, useState } from 'react'

import { PlusIcon } from '@/components/icons'
import { GrantProjectModelDialog } from '@/components/projects/GrantProjectModelDialog'
import { Button } from '@/components/ui/button'
import { Field, RequiredFieldLabel } from '@/components/ui/field'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { exactNameGlob, useTypeaheadSearch } from '@/hooks/use-resource-list'
import { useProjectPage } from '@/lib/use-project-page'

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

function useModelChoices(orgId: string, projectId: string, value: ModelSelection) {
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
  return { search, grantsQuery, selectedQuery, models, selected, displayedModels, unavailable }
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
  const modelTriggerRef = useRef<HTMLButtonElement>(null)
  const { search, grantsQuery, selectedQuery, models, selected, displayedModels, unavailable } =
    useModelChoices(orgId, projectId, value)
  useEffect(() => {
    onUnavailableChange?.(unavailable)
  }, [onUnavailableChange, unavailable])

  return (
    <>
      <Field>
        <RequiredFieldLabel htmlFor="agent-config-model">Model</RequiredFieldLabel>
        <ModelCombobox
          id="agent-config-model"
          triggerRef={modelTriggerRef}
          required
          items={displayedModels}
          value={selected}
          onValueChange={(model) => {
            if (model === null) {
              onChange({ providerConfig: '', modelName: '' })
              return
            }
            onChange({
              providerConfig: model.provider_config,
              modelName: model.name,
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
          action={
            project?.access.can_manage_access && (
              <Button
                variant="ghost"
                className="h-9 w-full justify-start px-2"
                onClick={() => {
                  setGrantOpen(true)
                }}
              >
                <PlusIcon className="size-4" />
                Grant models…
              </Button>
            )
          }
        />
        <ResourceNameFieldError value={value.providerConfig} fieldLabel="Provider config name" />
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
        onCloseAutoFocus={(event) => {
          event.preventDefault()
          modelTriggerRef.current?.focus()
        }}
        open={grantOpen}
        onOpenChange={setGrantOpen}
        orgId={orgId}
        projectId={projectId}
      />
    </>
  )
}
