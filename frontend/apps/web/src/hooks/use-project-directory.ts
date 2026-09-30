import { useProjects } from '@omnara/react'
import { useEffect } from 'react'

import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

export function useProjectDirectory(orgId: string) {
  const query = useProjects(orgId)
  const projects = useInfiniteQueryItems(query)
  const { fetchNextPage, hasNextPage, isFetchingNextPage } = query

  useEffect(() => {
    if (!hasNextPage || isFetchingNextPage) return
    void fetchNextPage()
  }, [fetchNextPage, hasNextPage, isFetchingNextPage])

  return new Map(projects.map((project) => [project.id, project]))
}
