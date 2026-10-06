import { ApiError } from '@omnara/sdk'

/**
 * Phase of a create-then-grant dialog. Once creation succeeds the dialog
 * moves to retry-grants, carrying the created resource so retrying failed
 * project grants never creates a duplicate.
 */
export type RetryGrantsPhase<T> =
  | { kind: 'form'; error: string }
  | { kind: 'retry-grants'; created: T; error: string }

export interface GrantFailures {
  /** Project IDs whose grant was rejected, in selection order. */
  failedProjectIds: string[]
  /** Sentence naming the failure count, e.g. "Sharing with 2 projects failed: …". */
  message: string
}

export interface BatchGrantFailures {
  failedIds: string[]
  message: string
}

export function collectBatchGrantFailures(
  ids: string[],
  results: PromiseSettledResult<unknown>[],
  describeFailed: (count: number) => string,
): BatchGrantFailures | null {
  const failed = results.flatMap((result, index) => {
    const id = ids[index]
    if (result.status !== 'rejected' || id === undefined) return []
    const cause: unknown = result.reason
    return [{ id, cause }]
  })
  if (failed.length === 0) return null
  // One project can fail for several resources, so count each ID once.
  const failedIds = [...new Set(failed.map((entry) => entry.id))]
  return {
    failedIds,
    message: `Sharing ${describeFailed(failedIds.length)} failed${grantFailureDetail(failed[0]?.cause)}. The failed selections are still selected — retry or remove them.`,
  }
}

/**
 * Pair Promise.allSettled grant results back with the project IDs that
 * produced them. A project ID may repeat when several resources are shared
 * with it; failures name each project once. Returns null when every grant
 * succeeded, so callers can keep the dialog open with the failed projects
 * still selected.
 */
export function collectGrantFailures(
  projectIds: string[],
  results: PromiseSettledResult<unknown>[],
): GrantFailures | null {
  const failures = collectBatchGrantFailures(
    projectIds,
    results,
    (count) => `with ${String(count)} project${count === 1 ? '' : 's'}`,
  )
  if (!failures) return null
  return {
    failedProjectIds: failures.failedIds,
    message: failures.message.replace('failed selections', 'failed projects'),
  }
}

function grantFailureDetail(cause: unknown) {
  if (cause instanceof ApiError || cause instanceof Error) {
    return cause.message ? `: ${cause.message}` : ''
  }
  return ''
}
