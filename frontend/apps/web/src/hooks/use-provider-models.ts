import { useConfiguredModels } from '@omnara/react'

import { useAllPages } from '@/hooks/use-all-pages'
import { clusterFirst } from '@/lib/management-kind'

/** All of a provider's models, sorted with Omnara-managed ones first. */
export function useProviderModels(orgId: string, providerId: string) {
  const query = useConfiguredModels(orgId, providerId)
  const pages = useAllPages(query)
  return {
    models: clusterFirst(
      [...pages.items].sort((left, right) => left.name.localeCompare(right.name)),
    ),
    isPending: pages.isPending,
    isError: pages.isError,
    refetch: () => {
      void query.refetch()
    },
  }
}
