import { useProjects } from '@omnara/react'
import { useParams } from '@tanstack/react-router'
import { useEffect } from 'react'

import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useActiveOrg } from '@/lib/use-active-org'

export function useProjectPage() {
  const { activeOrg } = useActiveOrg()
  const projectId = useParams({ strict: false }).projectId ?? ''
  const projectsQuery = useProjects(activeOrg.id)
  const project = useInfiniteQueryItems(projectsQuery).find(
    (candidate) => candidate.id === projectId,
  )
  const { fetchNextPage, hasNextPage, isFetchingNextPage } = projectsQuery

  useEffect(() => {
    if (!projectId || project || !hasNextPage || isFetchingNextPage) return
    void fetchNextPage()
  }, [fetchNextPage, hasNextPage, isFetchingNextPage, project, projectId])

  return {
    activeOrg,
    projectId,
    project,
    isPending: projectId !== '' && !project && hasNextPage,
  }
}
