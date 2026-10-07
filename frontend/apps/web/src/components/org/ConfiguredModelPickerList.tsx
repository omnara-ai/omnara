import type { DiscoveredProviderModel, ModelCatalog } from '@omnara/sdk'
import { type KeyboardEvent, type ReactNode, type Ref, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { errorMessage } from '@/lib/submit-status'

import { CustomModelRow, DiscoveredModelRow, SelectedModelRow } from './ConfiguredModelPickerRows'
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
  draftError,
  expandedId,
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
  onEditProvider,
}: {
  listRef: Ref<HTMLUListElement>
  providerName: string
  catalog: ModelCatalog | undefined
  catalogError: unknown
  loading: boolean
  empty: boolean
  search: string
  drafts: ConfiguredModelDraft[]
  draftError: (draft: ConfiguredModelDraft) => string
  expandedId: string | null
  availableModels: DiscoveredProviderModel[]
  /** Slugs the provider already has or that are picked, which can be configured again with other settings. */
  addedModels: DiscoveredProviderModel[]
  customSlug: string
  canAddCustom: boolean
  disabled: boolean
  onToggleExpanded: (id: string) => void
  onDraftChange: (draft: ConfiguredModelDraft) => void
  onRemoveDraft: (id: string) => void
  onSelect: (model: DiscoveredProviderModel) => void
  onAddCustom: () => void
  onRowKeyDown: (event: KeyboardEvent<HTMLElement>) => void
  /** Offered when the catalog fails, to go back and fix the provider's settings. */
  onEditProvider?: () => Promise<void>
}) {
  return (
    <>
      {catalog?.status === 'failed' && (
        <CatalogWarning disabled={disabled} onEditProvider={onEditProvider} />
      )}
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
                key={draft.id}
                draft={draft}
                error={draftError(draft)}
                index={index}
                expanded={expandedId === draft.id}
                disabled={disabled}
                onToggleExpanded={() => {
                  onToggleExpanded(draft.id)
                }}
                onChange={onDraftChange}
                onRowKeyDown={onRowKeyDown}
                onRemove={() => {
                  onRemoveDraft(draft.id)
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
              <DiscoveredModelRow
                key={model.slug}
                model={model}
                added
                disabled={disabled}
                onRowKeyDown={onRowKeyDown}
                onSelect={() => {
                  onSelect(model)
                }}
              />
            ))}
            {empty && <ListStatus>{emptyMessage(catalog, search)}</ListStatus>}
          </>
        )}
      </ul>
    </>
  )
}

function CatalogWarning({
  disabled,
  onEditProvider,
}: {
  disabled: boolean
  onEditProvider?: () => Promise<void>
}) {
  const [editing, setEditing] = useState(false)
  const [editError, setEditError] = useState('')

  async function editProvider() {
    if (!onEditProvider) return
    setEditing(true)
    setEditError('')
    try {
      await onEditProvider()
    } catch (error) {
      setEditError(errorMessage(error, 'Could not delete the model provider'))
      setEditing(false)
    }
  }

  return (
    <div role="alert" className="text-warning text-sm">
      <p className="font-medium">Warning: unable to fetch available models. This might mean:</p>
      <ul className="mt-2 list-disc space-y-1 pl-5">
        <li>Your API token is expired or invalid</li>
        <li>Your API base URL is invalid</li>
        <li>This API endpoint doesn&apos;t support listing models</li>
      </ul>
      <p className="mt-2">You can still add a model by entering its slug above.</p>
      {onEditProvider && (
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="mt-3"
          disabled={disabled || editing}
          loading={editing}
          onClick={() => {
            void editProvider()
          }}
        >
          Back to provider settings
        </Button>
      )}
      {editError && <p className="text-destructive mt-2">{editError}</p>}
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
