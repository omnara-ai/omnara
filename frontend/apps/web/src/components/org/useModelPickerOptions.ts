import { useConfiguredModels, useModelCatalog } from '@omnara/react'
import type { ModelCatalog } from '@omnara/sdk'

import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'

import {
  type ConfiguredModelDraft,
  discoveredModelMatches,
} from './CreateConfiguredModelDialogState'

/**
 * What the model picker can offer for one provider: catalog models not yet picked or
 * configured, configured models matching the search, and whether the search can be
 * added as a custom slug.
 */
export function useModelPickerOptions({
  orgId,
  providerId,
  initialCatalog,
  drafts,
  search,
}: {
  orgId: string
  providerId: string
  /** A catalog already fetched for this provider; skips fetching it again. */
  initialCatalog?: ModelCatalog
  drafts: ConfiguredModelDraft[]
  search: string
}) {
  const catalogQuery = useModelCatalog(orgId, providerId, { enabled: !initialCatalog })
  const catalog = initialCatalog ?? catalogQuery.data
  const catalogModels = catalog?.status === 'ok' ? (catalog.models ?? []) : []
  const configuredQuery = useConfiguredModels(orgId, providerId)
  const configured = useCompleteInfiniteQueryItems(configuredQuery, true)
  const addedSlugs = new Set(configured.items.map((model) => model.provider_model_slug))
  const unavailable = new Set([...addedSlugs, ...drafts.map((draft) => draft.slug)])
  const customSlug = search.trim()
  const availableModels = catalogModels.filter(
    (model) => !unavailable.has(model.slug) && discoveredModelMatches(model, search),
  )
  const addedModels = configured.items.filter((model) =>
    discoveredModelMatches({ slug: model.provider_model_slug, display_name: model.name }, search),
  )
  const canAddCustom =
    customSlug !== '' &&
    !unavailable.has(customSlug) &&
    !catalogModels.some((model) => model.slug === customSlug)
  return {
    catalog,
    catalogError: catalog === undefined && catalogQuery.isError ? catalogQuery.error : undefined,
    customSlug,
    canAddCustom,
    availableModels,
    addedModels,
    // Hold the list until both the catalog and existing models arrive, so rows don't shuffle in.
    loading: (catalog === undefined && catalogQuery.isPending) || configuredQuery.isPending,
    empty: drafts.length + availableModels.length + addedModels.length === 0 && !canAddCustom,
  }
}
