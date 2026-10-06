import { useProjects } from '@omnara/react'

import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

/**
 * Every project the viewer can see, keyed by id. The map stays empty until all pages have
 * loaded, so a missing project means no access rather than a page that hasn't arrived yet.
 * `isLoaded` stays true through background refetches, which keep the loaded pages.
 *
 * `loaded` holds the projects fetched so far, for rows from org-wide lists that only need
 * their own project and shouldn't wait on the rest.
 */
export function useProjectDirectory(orgId: string) {
  const query = useProjects(orgId)
  const { items, hasCompleteItems } = useCompleteInfiniteQueryItems(query, true)
  const loadedItems = useInfiniteQueryItems(query)
  return {
    projects: new Map(items.map((project) => [project.id, project])),
    loaded: new Map(loadedItems.map((project) => [project.id, project])),
    isLoaded: hasCompleteItems,
  }
}
