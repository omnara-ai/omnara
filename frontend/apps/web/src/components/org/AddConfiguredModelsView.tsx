import type { DiscoveredProviderModel, ModelProviderConfig } from '@omnara/sdk'
import { useState } from 'react'

import {
  ConfiguredModelPickerFooter,
  ConfiguredModelPickerHeader,
} from './ConfiguredModelPickerChrome'
import { ConfiguredModelPickerList } from './ConfiguredModelPickerList'
import {
  type ConfiguredModelDraft,
  configuredModelDraft,
  configuredModelDraftError,
} from './CreateConfiguredModelDialogState'
import { useConfiguredModelSubmission } from './useConfiguredModelSubmission'
import { useModelPickerKeyboard } from './useModelPickerKeyboard'
import { useModelPickerOptions } from './useModelPickerOptions'

/**
 * Pick models from a provider's catalog (or by slug), adjust each one's name and
 * token limits, and create them together, optionally sharing them with projects.
 */
export function AddConfiguredModelsView({
  orgId,
  providers,
  defaultProviderId,
  dismissLabel,
  onDone,
  onEditProvider,
}: {
  orgId: string
  providers: ModelProviderConfig[]
  defaultProviderId?: string
  /** Shows a secondary button that closes without adding models. */
  dismissLabel?: string
  onDone: () => void
  /** Discards the provider and returns to its settings, offered while its catalog fails. */
  onEditProvider?: () => Promise<void>
}) {
  const submission = useConfiguredModelSubmission(orgId, onDone)
  const [providerId, setProviderId] = useState(defaultProviderId ?? providers[0]?.id ?? '')
  const [search, setSearch] = useState('')
  const [drafts, setDrafts] = useState<ConfiguredModelDraft[]>([])
  const [expandedSlug, setExpandedSlug] = useState<string | null>(null)
  const provider = providers.find((candidate) => candidate.id === providerId)
  const {
    catalog,
    catalogError,
    customSlug,
    canAddCustom,
    availableModels,
    addedModels,
    existingNames,
    loading,
    empty,
  } = useModelPickerOptions({
    orgId,
    providerId,
    drafts,
    search,
  })
  const { retrying, submitting } = submission
  const locked = submitting || retrying
  const ready = drafts.length > 0 && drafts.every((draft) => !draftError(draft))

  /** Names a draft must not reuse: the provider's models and the other drafts. */
  function takenNames(except?: string) {
    const names = new Set(existingNames)
    for (const draft of drafts) if (draft.slug !== except) names.add(draft.name)
    return names
  }

  function draftError(draft: ConfiguredModelDraft) {
    return configuredModelDraftError(draft, takenNames(draft.slug))
  }

  function newDraft(model: DiscoveredProviderModel) {
    return configuredModelDraft(model, takenNames())
  }

  function switchProvider(nextProviderId: string) {
    setProviderId(nextProviderId)
    setDrafts([])
    setExpandedSlug(null)
    submission.clearError()
  }

  const {
    searchRef,
    listRef,
    holdFocusedRow,
    releaseHeldRow,
    onSearchKeyDown,
    onRowKeyDown,
    onViewKeyDown,
  } = useModelPickerKeyboard({
    onTypeAhead: (key) => {
      setSearch((current) => (key === 'Backspace' ? current.slice(0, -1) : current + key))
    },
    onSearchEnter: () => {
      // An exact slug wins over others it is a prefix of, like gpt-5 over gpt-5-mini.
      const matches = [...availableModels, ...addedModels]
      const [onlyMatch, ...others] = matches
      const match =
        matches.find((model) => model.slug === customSlug) ??
        (others.length === 0 ? onlyMatch : undefined)
      if (match) {
        select(newDraft(match))
        setSearch('')
      } else {
        addCustom()
      }
    },
    onSubmitShortcut: () => {
      if (retrying) void retrySharing()
      else if (ready && !submitting) void addModels()
    },
  })

  function select(draft: ConfiguredModelDraft) {
    holdFocusedRow()
    setDrafts((previous) => [...previous, draft])
    // Open the fields for a missing limit, or for a slug the provider already has, whose
    // second configuration is only worth adding with a different name or limits.
    const configured = addedModels.some((model) => model.slug === draft.slug)
    if (configured || draftError(draft)) setExpandedSlug(draft.slug)
  }

  function addCustom() {
    if (!canAddCustom) return
    releaseHeldRow()
    select(newDraft({ slug: customSlug }))
    setSearch('')
  }

  async function addModels() {
    const failed = await submission.add(providerId, drafts)
    setDrafts(failed)
    setExpandedSlug(null)
  }

  function retrySharing() {
    return submission.retrySharing(drafts.length)
  }

  return (
    // display: contents keeps the dialog's layout while catching ⌘/Ctrl+Enter anywhere in the view.
    // eslint-disable-next-line jsx-a11y/no-static-element-interactions -- a keyboard shortcut scope; every control inside stays individually focusable
    <div className="contents" onKeyDown={onViewKeyDown}>
      <ConfiguredModelPickerHeader
        providers={providers}
        providerId={providerId}
        onProviderChange={switchProvider}
        search={search}
        onSearchChange={setSearch}
        onSearchKeyDown={onSearchKeyDown}
        searchRef={searchRef}
        disabled={locked}
      />
      <ConfiguredModelPickerList
        listRef={listRef}
        providerName={provider?.name ?? 'the provider'}
        catalog={catalog}
        catalogError={catalogError}
        loading={loading}
        empty={empty}
        search={search}
        drafts={drafts}
        draftError={draftError}
        expandedSlug={expandedSlug}
        availableModels={availableModels}
        addedModels={addedModels}
        customSlug={customSlug}
        canAddCustom={canAddCustom}
        disabled={locked}
        onToggleExpanded={(slug) => {
          setExpandedSlug((current) => (current === slug ? null : slug))
        }}
        onDraftChange={(next) => {
          setDrafts((previous) =>
            previous.map((candidate) => (candidate.slug === next.slug ? next : candidate)),
          )
        }}
        onRemoveDraft={(slug) => {
          holdFocusedRow()
          setDrafts((previous) => previous.filter((candidate) => candidate.slug !== slug))
          setExpandedSlug((current) => (current === slug ? null : current))
        }}
        onSelect={(model) => {
          select(newDraft(model))
        }}
        onAddCustom={addCustom}
        onRowKeyDown={onRowKeyDown}
        // Once models are added the provider is in use, so it's no longer discarded.
        onEditProvider={submission.addedCount === 0 ? onEditProvider : undefined}
      />
      {submission.error && (
        <p role="alert" className="text-destructive text-sm">
          {submission.error}
        </p>
      )}
      <ConfiguredModelPickerFooter
        orgId={orgId}
        projectIds={submission.projectIds}
        onProjectIdsChange={submission.setProjectIds}
        failedProjectIds={submission.failedProjectIds}
        submitting={submitting}
        dismissLabel={dismissButtonLabel(dismissLabel, retrying, submission.addedCount)}
        onDismiss={onDone}
        primaryLabel={retrying ? 'Retry sharing' : addModelsLabel(drafts.length)}
        primaryDisabled={submitting || (!retrying && !ready)}
        onPrimary={() => {
          void (retrying ? retrySharing() : addModels())
        }}
      />
    </div>
  )
}

function addModelsLabel(count: number) {
  if (count === 0) return 'Add models'
  return `Add ${String(count)} ${count === 1 ? 'model' : 'models'}`
}

/** The secondary button's label, or undefined to hide it. */
function dismissButtonLabel(dismissLabel: string | undefined, retrying: boolean, added: number) {
  if (dismissLabel === undefined && !retrying) return undefined
  return added > 0 ? 'Done' : (dismissLabel ?? 'Done')
}
