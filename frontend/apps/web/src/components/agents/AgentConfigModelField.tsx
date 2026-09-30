import { exactNameGlob, useClusterModelPricing, useProjectModelGrants } from '@omnara/react'
import type {
  ConfiguredModelSummary,
  DiscoveredModelPricing,
  ProjectModelGrantEffectiveReasoning,
  ProjectModelGrantListItem,
} from '@omnara/sdk'
import { Brain } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'

import { PlusIcon } from '@/components/icons'
import { ModelPricingSummary } from '@/components/models/ModelPricing'
import { GrantProjectModelDialog } from '@/components/projects/GrantProjectModelDialog'
import { Button } from '@/components/ui/button'
import { Field, RequiredFieldLabel } from '@/components/ui/field'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'
import { useProjectPage } from '@/lib/use-project-page'

interface ModelChoice extends ConfiguredModelSummary {
  pricing: DiscoveredModelPricing | undefined
  reasoning: ProjectModelGrantEffectiveReasoning
}

const ModelCombobox = createResourceCombobox<ModelChoice>({
  itemKey: (model) => model.id,
  itemLabel: (model) => `${model.name} · ${model.provider_config}`,
  renderItem: (model) => (
    <span className="flex min-w-0 flex-1 flex-col gap-0.5">
      <span className="flex min-w-0 items-baseline gap-1.5">
        <span className="truncate">{model.name}</span>
        <span className="text-muted-foreground truncate text-xs">{model.provider_config}</span>
      </span>
      <span className="text-muted-foreground text-xs tabular-nums">
        <ModelPricingSummary pricing={model.pricing} /> per 1M tokens
      </span>
    </span>
  ),
  placeholder: 'Search granted models…',
  emptyMessage: 'No granted models found.',
})

export interface ModelSelection {
  providerConfig: string
  modelName: string
  reasoningEffort: string
}

// Models that support reasoning without listing efforts accept any value; offer
// the levels every reasoning API format understands.
const fallbackReasoningEfforts = ['low', 'medium', 'high']

function reasoningEffortOptions(reasoning: ProjectModelGrantEffectiveReasoning): string[] {
  if (!reasoning.supports_reasoning) return []
  return reasoning.supported_reasoning_efforts.length > 0
    ? reasoning.supported_reasoning_efforts
    : fallbackReasoningEfforts
}

function acceptsReasoningEffort(
  reasoning: ProjectModelGrantEffectiveReasoning,
  effort: string,
): boolean {
  if (!reasoning.supports_reasoning) return false
  const supported = reasoning.supported_reasoning_efforts
  return supported.length === 0 || supported.includes(effort)
}

function effortLabel(effort: string): string {
  if (effort === 'xhigh') return 'Extra High'
  return effort.charAt(0).toUpperCase() + effort.slice(1)
}

// Radix Select reserves the empty string, so the model default gets a sentinel.
const defaultEffortValue = '__default__'

function useModelChoices(orgId: string, projectId: string, value: ModelSelection) {
  const search = useTypeaheadSearch()
  const grantsQuery = useProjectModelGrants(orgId, projectId, {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
  })
  const pricing = useClusterModelPricing(orgId)
  const toChoice = ({ model, effective_reasoning }: ProjectModelGrantListItem): ModelChoice => ({
    ...model,
    pricing: pricing.pricingFor(model.model_provider_config_id, model.provider_model_slug),
    reasoning: effective_reasoning,
  })
  const models = useInfiniteQueryItems(grantsQuery).map(toChoice)
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
    listedSelected ?? completeSelection.items.map(toChoice).find(matchesValue) ?? null
  const displayedModels =
    selected && !models.some((model) => model.id === selected.id) ? [selected, ...models] : models
  const unavailable = lookupEnabled && completeSelection.isComplete && selected === null
  return {
    search,
    grantsQuery,
    selectedQuery,
    models,
    selected,
    displayedModels,
    unavailable,
    pricingPending: pricing.isPending,
  }
}

function EffortSelect({
  value,
  options,
  defaultEffort,
  invalid,
  disabled,
  onChange,
}: {
  value: string
  options: string[]
  defaultEffort: string
  invalid: boolean
  disabled: boolean
  onChange: (effort: string) => void
}) {
  const items = value === '' || options.includes(value) ? options : [...options, value]
  const defaultLabel = defaultEffort === '' ? 'Default' : `Default (${effortLabel(defaultEffort)})`
  return (
    <Select
      value={value === '' ? defaultEffortValue : value}
      disabled={disabled}
      onValueChange={(next) => {
        onChange(next === defaultEffortValue ? '' : next)
      }}
    >
      <SelectTrigger
        aria-label="Reasoning effort"
        title="Reasoning effort"
        aria-invalid={invalid ? true : undefined}
        className="w-38 -ml-px shrink-0 rounded-l-none focus-visible:z-10"
      >
        <span className="flex min-w-0 items-center gap-2">
          <Brain className="size-4" />
          <SelectValue>{value === '' ? 'Default' : effortLabel(value)}</SelectValue>
        </span>
      </SelectTrigger>
      <SelectContent align="end">
        <SelectItem value={defaultEffortValue}>{defaultLabel}</SelectItem>
        {items.map((effort) => (
          <SelectItem key={effort} value={effort}>
            {effortLabel(effort)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function selectionForModel(model: ModelChoice | null, reasoningEffort: string): ModelSelection {
  if (model === null) return { providerConfig: '', modelName: '', reasoningEffort: '' }
  return {
    providerConfig: model.provider_config,
    modelName: model.name,
    reasoningEffort: acceptsReasoningEffort(model.reasoning, reasoningEffort)
      ? reasoningEffort
      : '',
  }
}

function reasoningEffortState(model: ModelChoice | null, effort: string) {
  if (model === null) return { options: [], unsupported: false }
  return {
    options: reasoningEffortOptions(model.reasoning),
    unsupported: effort !== '' && !acceptsReasoningEffort(model.reasoning, effort),
  }
}

function modelPlaceholder(isPending: boolean, noneGranted: boolean): string {
  if (isPending) return 'Loading models…'
  return noneGranted ? 'No models granted' : 'Search granted models…'
}

function UnsupportedEffortError({
  effort,
  supportsReasoning,
}: {
  effort: string
  supportsReasoning: boolean
}) {
  return (
    <p className="text-destructive text-sm">
      {supportsReasoning
        ? `This model doesn’t support the “${effort}” reasoning effort.`
        : `This model doesn’t support reasoning, so the “${effort}” effort can’t be used.`}{' '}
      Pick another effort or use the default.
    </p>
  )
}

function ModelPricingHint({
  model,
  pricingPending,
}: {
  model: ModelChoice | null
  pricingPending: boolean
}) {
  return (
    <p className="text-muted-foreground flex h-4 items-center text-xs">
      {model && pricingPending && !model.pricing ? (
        <Skeleton className="h-3 w-36" />
      ) : model ? (
        <span>
          <ModelPricingSummary pricing={model.pricing} /> per 1M tokens
        </span>
      ) : null}
    </p>
  )
}

function QueryRetryError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <p className="text-destructive text-sm">
      {message}{' '}
      <button type="button" className="underline" onClick={onRetry}>
        Retry
      </button>
    </p>
  )
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
  const {
    search,
    grantsQuery,
    selectedQuery,
    models,
    selected,
    displayedModels,
    unavailable,
    pricingPending,
  } = useModelChoices(orgId, projectId, value)
  const { options: effortOptions, unsupported: effortUnsupported } = reasoningEffortState(
    selected,
    value.reasoningEffort,
  )
  useEffect(() => {
    onUnavailableChange?.(unavailable || effortUnsupported)
  }, [onUnavailableChange, unavailable, effortUnsupported])

  return (
    <>
      <Field>
        <RequiredFieldLabel htmlFor="agent-config-model">Model</RequiredFieldLabel>
        <div role="group" aria-label="Model and reasoning effort" className="flex min-w-0">
          <div className="min-w-0 flex-1">
            <ModelCombobox
              id="agent-config-model"
              triggerRef={modelTriggerRef}
              triggerClassName="rounded-r-none focus-visible:z-10"
              required
              items={displayedModels}
              value={selected}
              onValueChange={(model) => {
                onChange(selectionForModel(model, value.reasoningEffort))
              }}
              search={search}
              query={grantsQuery}
              placeholder={modelPlaceholder(
                grantsQuery.isPending,
                models.length === 0 && search.search === '',
              )}
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
          </div>
          <EffortSelect
            value={value.reasoningEffort}
            options={effortOptions}
            defaultEffort={selected?.reasoning.default_reasoning_effort ?? ''}
            invalid={effortUnsupported}
            disabled={selected === null || (effortOptions.length === 0 && !effortUnsupported)}
            onChange={(reasoningEffort) => {
              onChange({ ...value, reasoningEffort })
            }}
          />
        </div>
        <ModelPricingHint model={selected} pricingPending={pricingPending} />
        <ResourceNameFieldError value={value.providerConfig} fieldLabel="Provider config name" />
        <ResourceNameFieldError value={value.modelName} fieldLabel="Model name" />
        {effortUnsupported && (
          <UnsupportedEffortError
            effort={value.reasoningEffort}
            supportsReasoning={effortOptions.length > 0}
          />
        )}
        {unavailable && (
          <p className="text-destructive text-sm">
            The configured model “{value.modelName}” ({value.providerConfig}) is no longer available
            to the project. Pick another model or grant it again.
          </p>
        )}
        {grantsQuery.isError && (
          <QueryRetryError
            message="Could not load granted models."
            onRetry={() => {
              void grantsQuery.refetch()
            }}
          />
        )}
        {selectedQuery.isError && (
          <QueryRetryError
            message="Could not load the selected model."
            onRetry={() => {
              void selectedQuery.refetch()
            }}
          />
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
