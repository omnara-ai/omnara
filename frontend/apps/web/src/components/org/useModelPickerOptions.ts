import { useConfiguredModels, useModelCatalog } from '@omnara/react'
import type { DiscoveredProviderModel } from '@omnara/sdk'

import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'

import {
  type ConfiguredModelDraft,
  discoveredModelMatches,
} from './CreateConfiguredModelDialogState'

/**
 * What the model picker can offer for one provider: catalog models not yet picked or
 * configured, already-configured slugs that can be configured again with other settings,
 * and whether the search can be added as a custom slug.
 */
export function useModelPickerOptions({
  orgId,
  providerId,
  drafts,
  search,
}: {
  orgId: string
  providerId: string
  drafts: ConfiguredModelDraft[]
  search: string
}) {
  const catalogQuery = useModelCatalog(orgId, providerId)
  const catalog = catalogQuery.data
  const catalogModels = catalog?.status === 'ok' ? (catalog.models ?? []) : []
  const configuredQuery = useConfiguredModels(orgId, providerId)
  const configured = useCompleteInfiniteQueryItems(configuredQuery, true)
  const draftSlugs = new Set(drafts.map((draft) => draft.slug))
  // One row per configured slug, prefilled from its first configuration's limits.
  const configuredBySlug = new Map<string, DiscoveredProviderModel>()
  for (const model of configured.items) {
    if (configuredBySlug.has(model.provider_model_slug)) continue
    configuredBySlug.set(model.provider_model_slug, {
      slug: model.provider_model_slug,
      display_name: model.name,
      context_window_tokens: model.context_window_tokens,
      max_output_tokens: model.max_output_tokens ?? undefined,
    })
  }
  const unavailable = new Set([...configuredBySlug.keys(), ...draftSlugs])
  const customSlug = search.trim()
  const availableModels = catalogModels.filter(
    (model) => !unavailable.has(model.slug) && discoveredModelMatches(model, search),
  )
  const addedModels = [...configuredBySlug.values()].filter(
    (model) => !draftSlugs.has(model.slug) && discoveredModelMatches(model, search),
  )
  const canAddCustom =
    customSlug !== '' &&
    !unavailable.has(customSlug) &&
    !catalogModels.some((model) => model.slug === customSlug)
  return {
    catalog,
    catalogError: catalogQuery.isError ? catalogQuery.error : undefined,
    customSlug,
    canAddCustom,
    availableModels,
    addedModels,
    existingNames: new Set(configured.items.map((model) => model.name)),
    // Hold the list until both the catalog and existing models arrive, so rows don't shuffle in.
    loading: catalogQuery.isPending || configuredQuery.isPending,
    empty: drafts.length + availableModels.length + addedModels.length === 0 && !canAddCustom,
  }
}
