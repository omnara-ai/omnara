import type { ConfiguredModel, DiscoveredProviderModel, ModelCatalog } from '@omnara/sdk'
import type { KeyboardEvent, ReactNode, Ref } from 'react'

import { Spinner } from '@/components/ui/spinner'
import { errorMessage } from '@/lib/submit-status'

import {
  AddedModelRow,
  CustomModelRow,
  DiscoveredModelRow,
  SelectedModelRow,
} from './ConfiguredModelPickerRows'
import type { ConfiguredModelDraft } from './CreateConfiguredModelDialogState'

/**
 * The picker's list: models being added, catalog models to pick, a custom slug, and models
 * the provider already has, with loading, empty, and catalog-failure states.
 */
export function ConfiguredModelPickerList({
  listRef,
  providerName,
  catalog,
  catalogError,
  loading,
  empty,
  search,
  drafts,
  expandedSlug,
  availableModels,
  addedModels,
  customSlug,
  canAddCustom,
  disabled,
  onToggleExpanded,
  onDraftChange,
  onRemoveDraft,
  onSelect,
  onAddCustom,
  onRowKeyDown,
}: {
  listRef: Ref<HTMLUListElement>
  providerName: string
  catalog: ModelCatalog | undefined
  catalogError: unknown
  loading: boolean
  empty: boolean
  search: string
  drafts: ConfiguredModelDraft[]
  expandedSlug: string | null
  availableModels: DiscoveredProviderModel[]
  addedModels: ConfiguredModel[]
  customSlug: string
  canAddCustom: boolean
  disabled: boolean
  onToggleExpanded: (slug: string) => void
  onDraftChange: (draft: ConfiguredModelDraft) => void
  onRemoveDraft: (slug: string) => void
  onSelect: (model: DiscoveredProviderModel) => void
  onAddCustom: () => void
  onRowKeyDown: (event: KeyboardEvent<HTMLElement>) => void
}) {
  return (
    <>
      {catalog?.status === 'failed' && <CatalogWarning />}
      {catalogError !== undefined && (
        <p role="alert" className="text-destructive text-sm">
          {errorMessage(catalogError, 'Could not load the provider’s models.')} You can still add a
          model by entering its slug.
        </p>
      )}
      <ul
        ref={listRef}
        aria-label="Models"
        className="-mx-3 flex max-h-[min(50vh,28rem)] flex-col overflow-y-auto"
      >
        {loading ? (
          <ListStatus>
            <Spinner className="size-4" />
            Loading models from {providerName}…
          </ListStatus>
        ) : (
          <>
            {drafts.map((draft, index) => (
              <SelectedModelRow
                key={draft.slug}
                draft={draft}
                index={index}
                expanded={expandedSlug === draft.slug}
                disabled={disabled}
                onToggleExpanded={() => {
                  onToggleExpanded(draft.slug)
                }}
                onChange={onDraftChange}
                onRowKeyDown={onRowKeyDown}
                onRemove={() => {
                  onRemoveDraft(draft.slug)
                }}
              />
            ))}
            {availableModels.map((model) => (
              <DiscoveredModelRow
                key={model.slug}
                model={model}
                disabled={disabled}
                onRowKeyDown={onRowKeyDown}
                onSelect={() => {
                  onSelect(model)
                }}
              />
            ))}
            {canAddCustom && (
              <CustomModelRow
                slug={customSlug}
                disabled={disabled}
                onAdd={onAddCustom}
                onRowKeyDown={onRowKeyDown}
              />
            )}
            {addedModels.map((model) => (
              <AddedModelRow key={model.id} slug={model.provider_model_slug} name={model.name} />
            ))}
            {empty && <ListStatus>{emptyMessage(catalog, search)}</ListStatus>}
          </>
        )}
      </ul>
    </>
  )
}

function CatalogWarning() {
  return (
    <div role="alert" className="text-warning text-sm">
      <p className="font-medium">Warning: unable to fetch available models. This might mean:</p>
      <ul className="mt-2 list-disc space-y-1 pl-5">
        <li>Your API token is expired or invalid</li>
        <li>Your API base URL is invalid</li>
        <li>This API endpoint doesn&apos;t support listing models</li>
      </ul>
      <p className="mt-2">You can still add a model by entering its slug above.</p>
    </div>
  )
}

function ListStatus({ children }: { children: ReactNode }) {
  return (
    <li
      role="status"
      className="text-muted-foreground flex min-h-32 items-center justify-center gap-2 px-6 text-center text-sm"
    >
      {children}
    </li>
  )
}

function emptyMessage(catalog: ModelCatalog | undefined, search: string) {
  if (catalog?.status !== 'ok') return 'Enter a model slug above to add one.'
  if (search.trim() !== '') return 'No models match your search.'
  return 'This provider didn’t report any models. Enter a model slug above to add one.'
}
