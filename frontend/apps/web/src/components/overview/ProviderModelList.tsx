import type { ModelPricingLookup } from '@omnara/react'
import type { ConfiguredModel, ModelProviderConfig } from '@omnara/sdk'
import { type ReactNode, useId, useState } from 'react'

import { OmnaraManagedTag } from '@/components/brand/OmnaraManaged'
import { DetailList } from '@/components/data-table/DetailList'
import { ChevronDown, Plus } from '@/components/icons'
import { ModelPricingSummary } from '@/components/models/ModelPricing'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { Button } from '@/components/ui/button'
import { formatCount, formatDateTime } from '@/lib/format'
import { modelPricingDetailItems } from '@/lib/model-pricing'
import { cn } from '@/lib/utils'

export interface ModelActions {
  onCreate?: (providerId: string) => void
  onEdit?: (model: ConfiguredModel) => void
  onGrant?: (model: ConfiguredModel) => void
  onDelete?: (provider: ModelProviderConfig, model: ConfiguredModel) => void
}

/** Opens the add-model dialog for a provider; shown above its full model list. */
export function AddModelButton({
  providerId,
  onCreate,
}: {
  providerId: string
  onCreate: (providerId: string) => void
}) {
  return (
    <Button
      size="sm"
      variant="outline"
      onClick={() => {
        onCreate(providerId)
      }}
    >
      <Plus aria-hidden="true" />
      Add model
    </Button>
  )
}

export function ProviderModelList({
  provider,
  models,
  isPending,
  isError,
  onRetry,
  pricing,
  actions,
  viewAll,
}: {
  provider: ModelProviderConfig
  models: ConfiguredModel[]
  isPending: boolean
  isError: boolean
  onRetry: () => void
  pricing: ModelPricingLookup
  actions: ModelActions
  /** Link to the full list, when the preview leaves models out. */
  viewAll?: ReactNode
}) {
  const { onCreate } = actions

  return (
    <div className="flex flex-col gap-1 border-t px-2 py-2">
      {isError ? (
        <div className="text-muted-foreground flex items-center justify-between gap-2 px-2 py-1.5 text-sm">
          Couldn&rsquo;t load models.
          <Button size="sm" variant="outline" onClick={onRetry}>
            Retry
          </Button>
        </div>
      ) : models.length === 0 ? (
        <p className="text-muted-foreground px-2 py-1.5 text-sm">
          {isPending ? 'Loading models…' : 'No models yet. Add one so agents can use it.'}
        </p>
      ) : (
        <ul className="flex flex-col gap-0.5">
          {models.map((model) => (
            <ModelRow
              key={model.id}
              provider={provider}
              model={model}
              pricing={pricing}
              actions={actions}
            />
          ))}
        </ul>
      )}
      {(onCreate !== undefined || viewAll) && (
        <div className="flex items-center justify-between gap-2 px-1">
          {onCreate ? (
            <Button
              size="sm"
              variant="ghost"
              className="text-primary hover:text-primary h-9 px-2 sm:h-7"
              onClick={() => {
                onCreate(provider.id)
              }}
            >
              <Plus aria-hidden="true" />
              Add model
            </Button>
          ) : (
            <span />
          )}
          {viewAll}
        </div>
      )}
    </div>
  )
}

function ModelRow({
  provider,
  model,
  pricing,
  actions,
}: {
  provider: ModelProviderConfig
  model: ConfiguredModel
  pricing: ModelPricingLookup
  actions: ModelActions
}) {
  const [open, setOpen] = useState(false)
  const detailsId = useId()
  const modelPricing = pricing.pricingFor(provider.id, model.provider_model_slug)
  const tenant = model.management_kind === 'tenant'
  const { onEdit, onGrant, onDelete } = actions

  return (
    <li className="flex flex-col">
      <div className="hover:bg-accent flex min-w-0 items-center gap-2 rounded-md pr-1 transition-colors">
        <button
          type="button"
          aria-expanded={open}
          aria-controls={detailsId}
          onClick={() => {
            setOpen((value) => !value)
          }}
          className="flex min-w-0 flex-1 items-center gap-2.5 px-2 py-1.5 text-left text-sm"
        >
          <ChevronDown
            className={cn(
              'text-muted-foreground size-3.5 shrink-0 transition-transform duration-200 motion-reduce:transition-none',
              !open && '-rotate-90',
            )}
            aria-hidden="true"
          />
          <span className="truncate font-medium">{model.name}</span>
          {model.management_kind === 'cluster' && <OmnaraManagedTag />}
          <span className="text-muted-foreground hidden truncate font-mono text-xs sm:inline">
            {model.provider_model_slug}
          </span>
          <span className="text-muted-foreground ml-auto hidden shrink-0 text-xs tabular-nums md:inline">
            {formatCount(model.context_window_tokens)} ctx
          </span>
          <ModelPricingSummary
            className="text-muted-foreground shrink-0 whitespace-nowrap text-xs tabular-nums max-md:ml-auto"
            pricing={modelPricing}
          />
        </button>
        <ResourceRowActions
          onEdit={
            tenant && onEdit
              ? () => {
                  onEdit(model)
                }
              : undefined
          }
          onGrant={
            onGrant
              ? () => {
                  onGrant(model)
                }
              : undefined
          }
          onDelete={
            tenant && onDelete
              ? () => {
                  onDelete(provider, model)
                }
              : undefined
          }
        />
      </div>
      {open && (
        <div id={detailsId} className="py-2 pl-8 pr-2">
          <DetailList
            items={[
              { label: 'ID', value: model.id, mono: true },
              { label: 'Provider model', value: model.provider_model_slug, mono: true },
              ...modelPricingDetailItems(modelPricing),
              {
                label: 'Context window',
                value: `${model.context_window_tokens.toLocaleString()} tokens`,
              },
              {
                label: 'Max output',
                value: model.max_output_tokens
                  ? `${model.max_output_tokens.toLocaleString()} tokens`
                  : undefined,
              },
              {
                label: 'Default max output',
                value: model.default_max_output_tokens
                  ? `${model.default_max_output_tokens.toLocaleString()} tokens`
                  : undefined,
              },
              { label: 'Tools', value: model.supports_tools ? 'Supported' : 'Not supported' },
              { label: 'Created', value: formatDateTime(model.created_at) },
              { label: 'Updated', value: formatDateTime(model.updated_at) },
            ]}
          />
        </div>
      )}
    </li>
  )
}
