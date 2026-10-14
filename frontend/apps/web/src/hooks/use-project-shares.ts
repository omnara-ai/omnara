import { useState } from 'react'

import { collectGrantFailures } from '@/lib/grant-failures'

interface ProjectShare {
  resourceId: string
  projectId: string
}

/** Resources whose sharing hasn't finished, and the shares of theirs that already landed. */
interface UnfinishedShares {
  resourceIds: string[]
  granted: string[]
  failedProjectIds: string[]
  error: string
}

function shareKey({ resourceId, projectId }: ProjectShare) {
  return `${resourceId}\u0000${projectId}`
}

/**
 * Projects to share newly created resources with, and the shares that still need a
 * retry after some of them failed. The failed projects stay selected (and are reported
 * through failedProjectIds) so they can be retried or removed; a retry shares the
 * created resources with every selected project they don't already reach, so removing
 * a failed project drops its shares and adding one shares with it too.
 */
export function useProjectShares(grant: (share: ProjectShare) => Promise<void>) {
  const [projectIds, setProjectIds] = useState<string[]>([])
  const [unfinished, setUnfinished] = useState<UnfinishedShares | null>(null)
  const [sharing, setSharing] = useState(false)

  async function shareWithSelection(resourceIds: string[], granted: ReadonlySet<string>) {
    const shares = resourceIds.flatMap((resourceId) =>
      projectIds.flatMap((projectId) => {
        const share = { resourceId, projectId }
        return granted.has(shareKey(share)) ? [] : [share]
      }),
    )
    if (shares.length === 0) {
      setUnfinished(null)
      return true
    }
    setSharing(true)
    setUnfinished((previous) => previous && { ...previous, error: '' })
    // allSettled never rejects, so sharing always ends here.
    const results = await Promise.allSettled(shares.map((share) => grant(share)))
    setSharing(false)
    const failures = collectGrantFailures(
      shares.map((share) => share.projectId),
      results,
    )
    if (!failures) {
      setUnfinished(null)
      return true
    }
    const landed = shares.filter((_, index) => results[index]?.status === 'fulfilled')
    setUnfinished({
      resourceIds,
      granted: [...granted, ...landed.map(shareKey)],
      failedProjectIds: failures.failedProjectIds,
      error: failures.message,
    })
    return false
  }

  return {
    projectIds,
    setProjectIds,
    sharing,
    /** True while some shares failed and still need a retry. */
    retrying: unfinished !== null,
    failedProjectIds: unfinished?.failedProjectIds ?? [],
    /** How many created resources still have shares to retry. */
    unfinishedCount: unfinished?.resourceIds.length ?? 0,
    error: unfinished?.error ?? '',
    /** Shares the resources with the selected projects; true when every share succeeded. */
    share: (resourceIds: string[]) => shareWithSelection(resourceIds, new Set()),
    /** Retries the unfinished shares against the current selection; true when all succeed. */
    retry: () =>
      unfinished
        ? shareWithSelection(unfinished.resourceIds, new Set(unfinished.granted))
        : Promise.resolve(true),
    reset: () => {
      setProjectIds([])
      setUnfinished(null)
    },
  }
}
