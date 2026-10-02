import { useProjects } from '@omnara/react'

import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'

/**
 * Every project the viewer can see, keyed by id. The map stays empty until all pages have
 * loaded, so a missing project means no access rather than a page that hasn't arrived yet.
 * `isLoaded` stays true through background refetches, which keep the loaded pages.
 */
export function useProjectDirectory(orgId: string) {
  const { items, hasCompleteItems } = useCompleteInfiniteQueryItems(useProjects(orgId), true)
  return {
    projects: new Map(items.map((project) => [project.id, project])),
    isLoaded: hasCompleteItems,
  }
}
