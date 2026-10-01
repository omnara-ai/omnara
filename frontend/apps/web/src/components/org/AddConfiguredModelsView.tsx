import {
  useConfiguredModels,
  useCreateConfiguredModel,
  useCreateProjectModelGrant,
  useModelCatalog,
} from '@omnara/react'
import type { ModelCatalog, ModelProviderConfig } from '@omnara/sdk'
import { type KeyboardEvent, type ReactNode, useEffect, useRef, useState } from 'react'

import { ProjectShareChips } from '@/components/projects/ProjectShareChips'
import { Button } from '@/components/ui/button'
import { DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { errorMessage } from '@/lib/submit-status'

import {
  AddedModelRow,
  CustomModelRow,
  DiscoveredModelRow,
  SelectedModelRow,
} from './ConfiguredModelPickerRows'
import {
  type ConfiguredModelDraft,
  configuredModelDraft,
  configuredModelDraftError,
  configuredModelDraftRequest,
  discoveredModelMatches,
} from './CreateConfiguredModelDialogState'

interface PendingGrant {
  modelId: string
  projectId: string
}

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
  const createConfiguredModel = useCreateConfiguredModel(orgId)
  const createProjectModelGrant = useCreateProjectModelGrant(orgId)
  const [providerId, setProviderId] = useState(defaultProviderId ?? providers[0]?.id ?? '')
  const [search, setSearch] = useState('')
  const [drafts, setDrafts] = useState<ConfiguredModelDraft[]>([])
  const [expandedSlug, setExpandedSlug] = useState<string | null>(null)
  const [projectIds, setProjectIds] = useState<string[]>([])
  const [pendingGrants, setPendingGrants] = useState<PendingGrant[]>([])
  const [addedCount, setAddedCount] = useState(0)
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const searchRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLUListElement>(null)
  // Row to focus after a toggle moves the focused row elsewhere in the list.
  const refocusRow = useRef<number | null>(null)

  useEffect(() => {
    searchRef.current?.focus()
  }, [])

  useEffect(() => {
    const index = refocusRow.current
    if (index === null) return
    refocusRow.current = null
    const rows = pickerRows()
    ;(rows[Math.min(index, rows.length - 1)] ?? searchRef.current)?.focus()
  })

  const provider = providers.find((candidate) => candidate.id === providerId)
  const preloaded = initialCatalog !== undefined && providerId === defaultProviderId
  const catalogQuery = useModelCatalog(orgId, providerId, { enabled: !preloaded })
  const catalog = preloaded ? initialCatalog : catalogQuery.data
  const catalogModels = catalog?.status === 'ok' ? (catalog.models ?? []) : []
  const configuredQuery = useConfiguredModels(orgId, providerId)
  const configured = useCompleteInfiniteQueryItems(configuredQuery, true)
  const addedSlugs = new Set(configured.items.map((model) => model.provider_model_slug))
  const draftSlugs = new Set(drafts.map((draft) => draft.slug))
  const customSlug = search.trim()
  const canAddCustom =
    customSlug !== '' &&
    !draftSlugs.has(customSlug) &&
    !addedSlugs.has(customSlug) &&
    !catalogModels.some((model) => model.slug === customSlug)
  const availableModels = catalogModels.filter(
    (model) =>
      !draftSlugs.has(model.slug) &&
      !addedSlugs.has(model.slug) &&
      discoveredModelMatches(model, search),
  )
  const addedModels = configured.items.filter((model) =>
    discoveredModelMatches({ slug: model.provider_model_slug, display_name: model.name }, search),
  )
  // Hold the list until both the catalog and existing models arrive, so rows don't shuffle in.
  const loading = (catalog === undefined && catalogQuery.isPending) || configuredQuery.isPending
  const empty = drafts.length + availableModels.length + addedModels.length === 0 && !canAddCustom
  const retrying = pendingGrants.length > 0
  const locked = submitting || retrying
  const ready = drafts.length > 0 && drafts.every((draft) => !configuredModelDraftError(draft))

  function switchProvider(nextProviderId: string) {
    setProviderId(nextProviderId)
    setDrafts([])
    setExpandedSlug(null)
    setError('')
  }

  function pickerRows() {
    return [...(listRef.current?.querySelectorAll<HTMLElement>('[data-picker-row]') ?? [])]
  }

  /** Keeps keyboard focus at the same list position when a toggle moves the focused row. */
  function holdFocusedRow() {
    const focused = document.activeElement
    const index = pickerRows().findIndex((row) => row === focused)
    if (index !== -1) refocusRow.current = index
  }

  function select(draft: ConfiguredModelDraft) {
    holdFocusedRow()
    setDrafts((previous) => [...previous, draft])
    if (configuredModelDraftError(draft)) setExpandedSlug(draft.slug)
  }

  function addCustom() {
    if (!canAddCustom) return
    refocusRow.current = null
    select(configuredModelDraft({ slug: customSlug }))
    setSearch('')
  }

  function onSearchKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      pickerRows()[0]?.focus()
      return
    }
    if (event.key !== 'Enter' || event.metaKey || event.ctrlKey) return
    event.preventDefault()
    const [onlyMatch] = availableModels
    if (availableModels.length === 1 && onlyMatch) {
      select(configuredModelDraft(onlyMatch))
      setSearch('')
    } else {
      addCustom()
    }
  }

  function onRowKeyDown(event: KeyboardEvent<HTMLElement>) {
    const row = event.currentTarget
    const rows = pickerRows()
    const index = rows.indexOf(row)
    const focusRow = (next: HTMLElement | null | undefined) => {
      event.preventDefault()
      next?.focus()
    }
    if (event.key === 'ArrowDown') focusRow(rows[index + 1])
    else if (event.key === 'ArrowUp') focusRow(index === 0 ? searchRef.current : rows[index - 1])
    else if (event.key === 'Home') focusRow(rows[0])
    else if (event.key === 'End') focusRow(rows.at(-1))
    else if (event.key === 'Enter' && !event.metaKey && !event.ctrlKey) {
      event.preventDefault()
      row.click()
    } else if ((event.key.length === 1 && event.key !== ' ') || event.key === 'Backspace') {
      if (event.metaKey || event.ctrlKey || event.altKey) return
      event.preventDefault()
      const key = event.key
      setSearch((current) => (key === 'Backspace' ? current.slice(0, -1) : current + key))
      searchRef.current?.focus()
    }
  }

  function onViewKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key !== 'Enter' || !(event.metaKey || event.ctrlKey)) return
    event.preventDefault()
    if (submitting) return
    if (retrying) void retrySharing()
    else if (ready) void addModels()
  }

  async function share(grants: PendingGrant[]) {
    const results = await Promise.allSettled(
      grants.map((grant) =>
        createProjectModelGrant.mutateAsync({
          projectID: grant.projectId,
          configured_model_id: grant.modelId,
        }),
      ),
    )
    return grants.filter((_, index) => results[index]?.status === 'rejected')
  }

  async function addModels() {
    const batch = drafts
    setSubmitting(true)
    setError('')
    try {
      const results = await Promise.allSettled(
        batch.map((draft) =>
          createConfiguredModel.mutateAsync({
            modelProviderConfigID: providerId,
            ...configuredModelDraftRequest(draft),
          }),
        ),
      )
      const created = results.flatMap((result) =>
        result.status === 'fulfilled' ? [result.value] : [],
      )
      const failed = batch.filter((_, index) => results[index]?.status === 'rejected')
      const firstFailure = results.find((result) => result.status === 'rejected')
      setDrafts(failed)
      setExpandedSlug(null)
      setAddedCount((count) => count + created.length)
      const failedGrants = await share(
        created.flatMap((model) =>
          projectIds.map((projectId) => ({ modelId: model.id, projectId })),
        ),
      )
      setPendingGrants(failedGrants)
      const messages = []
      if (failed.length > 0) {
        messages.push(
          `Added ${String(created.length)} of ${String(batch.length)} models. ` +
            errorMessage(firstFailure?.reason, 'The remaining models could not be added.'),
        )
      }
      if (failedGrants.length > 0) messages.push(sharingFailure(failedGrants.length))
      if (messages.length === 0) {
        onDone()
        return
      }
      setError(messages.join(' '))
    } finally {
      setSubmitting(false)
    }
  }

  async function retrySharing() {
    setSubmitting(true)
    setError('')
    try {
      const failedGrants = await share(pendingGrants)
      setPendingGrants(failedGrants)
      if (failedGrants.length > 0) {
        setError(sharingFailure(failedGrants.length))
      } else if (drafts.length === 0) {
        onDone()
      }
    } finally {
      setSubmitting(false)
    }
  }

  return (
    // display: contents keeps the dialog's layout while catching ⌘/Ctrl+Enter anywhere in the view.
    // eslint-disable-next-line jsx-a11y/no-static-element-interactions -- a keyboard shortcut scope; every control inside stays individually focusable
    <div className="contents" onKeyDown={onViewKeyDown}>
      <DialogHeader className="sr-only">
        <DialogTitle>Add models</DialogTitle>
        <DialogDescription>
          Choose models from {provider?.name ?? 'the provider'} to configure.
        </DialogDescription>
      </DialogHeader>
      <div className="-mx-4 -mt-4 flex items-center gap-2 border-b py-3 pl-4 pr-12 sm:-mx-6 sm:-mt-6 sm:py-4 sm:pl-6 sm:pr-14">
        {providers.length > 1 ? (
          <Select value={providerId} disabled={locked} onValueChange={switchProvider}>
            <SelectTrigger
              aria-label="Provider"
              className="text-muted-foreground h-8 w-auto max-w-48 shrink-0 border-0 bg-transparent px-1.5 shadow-none"
            >
              <SelectValue>{provider?.name}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {providers.map((option) => (
                <SelectItem key={option.id} value={option.id}>
                  {option.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : (
          <span className="text-muted-foreground max-w-48 shrink-0 truncate text-sm">
            {provider?.name}
          </span>
        )}
        <span aria-hidden="true" className="text-muted-foreground/50">
          /
        </span>
        <input
          ref={searchRef}
          aria-label="Search models"
          autoComplete="off"
          spellCheck={false}
          value={search}
          disabled={locked}
          placeholder="Search models or enter a slug"
          className="placeholder:text-muted-foreground min-w-0 flex-1 bg-transparent font-mono text-sm outline-none placeholder:font-sans"
          onChange={(event) => {
            setSearch(event.target.value)
          }}
          onKeyDown={onSearchKeyDown}
        />
      </div>
      {catalog?.status === 'failed' && <CatalogWarning />}
      {catalog === undefined && catalogQuery.isError && (
        <p role="alert" className="text-destructive text-sm">
          {errorMessage(catalogQuery.error, 'Could not load the provider’s models.')} You can still
          add a model by entering its slug.
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
            Loading models from {provider?.name ?? 'the provider'}…
          </ListStatus>
        ) : (
          <>
            {drafts.map((draft, index) => (
              <SelectedModelRow
                key={draft.slug}
                draft={draft}
                index={index}
                expanded={expandedSlug === draft.slug}
                disabled={locked}
                onToggleExpanded={() => {
                  setExpandedSlug((current) => (current === draft.slug ? null : draft.slug))
                }}
                onChange={(next) => {
                  setDrafts((previous) =>
                    previous.map((candidate) => (candidate.slug === draft.slug ? next : candidate)),
                  )
                }}
                onRowKeyDown={onRowKeyDown}
                onRemove={() => {
                  holdFocusedRow()
                  setDrafts((previous) =>
                    previous.filter((candidate) => candidate.slug !== draft.slug),
                  )
                  if (expandedSlug === draft.slug) setExpandedSlug(null)
                }}
              />
            ))}
            {availableModels.map((model) => (
              <DiscoveredModelRow
                key={model.slug}
                model={model}
                disabled={locked}
                onRowKeyDown={onRowKeyDown}
                onSelect={() => {
                  select(configuredModelDraft(model))
                }}
              />
            ))}
            {canAddCustom && (
              <CustomModelRow
                slug={customSlug}
                disabled={locked}
                onAdd={addCustom}
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
      {error && (
        <p role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}
      <div className="-mx-4 -mb-4 flex flex-wrap items-center gap-3 border-t px-4 py-3 sm:-mx-6 sm:-mb-6 sm:px-6 sm:py-4">
        <ProjectShareChips
          orgId={orgId}
          value={projectIds}
          onChange={setProjectIds}
          disabled={locked}
          isProjectEligible={(project) => project.access.can_manage_access}
        />
        <div className="ml-auto flex gap-2">
          {(dismissLabel !== undefined || retrying) && (
            <Button type="button" variant="ghost" disabled={submitting} onClick={onDone}>
              {addedCount > 0 ? 'Done' : (dismissLabel ?? 'Done')}
            </Button>
          )}
          <Button
            type="button"
            aria-keyshortcuts="Meta+Enter Control+Enter"
            disabled={submitting || (!retrying && !ready)}
            loading={submitting}
            onClick={() => {
              void (retrying ? retrySharing() : addModels())
            }}
          >
            {retrying ? 'Retry sharing' : addModelsLabel(drafts.length)}
          </Button>
        </div>
      </div>
    </div>
  )
}

function addModelsLabel(count: number) {
  if (count === 0) return 'Add models'
  return `Add ${String(count)} ${count === 1 ? 'model' : 'models'}`
}

function sharingFailure(count: number) {
  return `Sharing failed for ${String(count)} ${count === 1 ? 'project' : 'projects'}; retry to finish sharing.`
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
