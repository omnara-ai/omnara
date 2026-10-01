import type { ModelCatalog, ModelProviderConfig } from '@omnara/sdk'
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
  initialCatalog,
  dismissLabel,
  onDone,
}: {
  orgId: string
  providers: ModelProviderConfig[]
  defaultProviderId?: string
  /** A catalog already fetched for defaultProviderId, such as the one returned on creation. */
  initialCatalog?: ModelCatalog
  /** Shows a secondary button that closes without adding models. */
  dismissLabel?: string
  onDone: () => void
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
    loading,
    empty,
  } = useModelPickerOptions({
    orgId,
    providerId,
    initialCatalog: providerId === defaultProviderId ? initialCatalog : undefined,
    drafts,
    search,
  })
  const { retrying, submitting } = submission
  const locked = submitting || retrying
  const ready = drafts.length > 0 && drafts.every((draft) => !configuredModelDraftError(draft))

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
      const [onlyMatch, ...others] = availableModels
      if (onlyMatch && others.length === 0) {
        select(configuredModelDraft(onlyMatch))
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
    if (configuredModelDraftError(draft)) setExpandedSlug(draft.slug)
  }

  function addCustom() {
    if (!canAddCustom) return
    releaseHeldRow()
    select(configuredModelDraft({ slug: customSlug }))
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
          select(configuredModelDraft(model))
        }}
        onAddCustom={addCustom}
        onRowKeyDown={onRowKeyDown}
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
        locked={locked}
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
