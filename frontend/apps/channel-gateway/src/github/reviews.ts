import {
  type GitHubReviewObservation,
  type GitHubReviewOperationScope,
  type GitHubReviewOwnershipObservation,
  schemas,
} from '@omnara/sdk'

import type { CoreClient } from '../core-client'
import type { GitHubReviewInstallation } from '../core-github-reviews'
import type { OperationAttemptContext } from '../operation-retry'
import type { GitHubClient } from './client'
import { GitHubAPIError, type GitHubReview } from './protocol'
import { listGitHubPendingReviews } from './read'

export type GitHubReviewCore = Pick<CoreClient, 'lookupGitHubReviews' | 'recordGitHubReview'>
export interface GitHubReviewAuthority {
  core: GitHubReviewCore
  installation: GitHubReviewInstallation
  scope: GitHubReviewOperationScope
}

export function githubReviewMarker(requestID: string): string {
  if (!schemas.zToolCallId.safeParse(requestID).success) throw new GitHubAPIError('invalid_request')
  return `<!-- omnara-review:${requestID} -->`
}

/** Only an exact, entire marker body is positive correlation evidence. */
export function githubReviewObservation(review: GitHubReview): GitHubReviewObservation {
  const match = /^<!-- omnara-review:(tcl_[a-z2-7]{26}) -->$/.exec(review.body)
  return {
    review_id: review.id,
    commit_id: review.commit?.oid,
    creating_tool_call_id: match?.[1],
  }
}

export async function lookupGitHubOwnership(
  authority: GitHubReviewAuthority,
  observations: GitHubReviewObservation[],
  context: OperationAttemptContext,
): Promise<GitHubReviewOwnershipObservation[]> {
  try {
    const result = await authority.core.lookupGitHubReviews(
      authority.installation,
      {
        scope: authority.scope,
        observations,
      },
      context.signal,
    )
    return result.observations
  } catch {
    throw new GitHubAPIError('review_state_unavailable')
  }
}

export async function observedGitHubPending(
  client: GitHubClient,
  number: number,
  authority: GitHubReviewAuthority,
  context: OperationAttemptContext,
) {
  const page = await listGitHubPendingReviews(client, number, context)
  if (!page.complete) throw new GitHubAPIError('review_state_unavailable')
  const newest = [...page.reviews].sort(
    (a, b) =>
      Date.parse(b.createdAt) - Date.parse(a.createdAt) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0),
  )
  const observations = newest.map(githubReviewObservation)
  const ownership = observations.length
    ? await lookupGitHubOwnership(authority, observations, context)
    : []
  return { observations, ownership }
}

/** Identity recording is idempotent factual bookkeeping, not new send authority.
 * A late known create response gets one small callback window independent of the
 * expired native deadline. The caller must still stop before any next mutation.
 */
export async function recordGitHubCreation(
  authority: GitHubReviewAuthority,
  reviewID: string,
  commitID: string,
  context: OperationAttemptContext,
): Promise<boolean> {
  const late = context.signal.aborted || Date.now() >= context.deadlineMs
  const signal = late ? AbortSignal.timeout(2_000) : context.signal
  try {
    const recorded = await authority.core.recordGitHubReview(
      authority.installation,
      {
        scope: authority.scope,
        observation: {
          review_id: reviewID,
          commit_id: commitID,
          creating_tool_call_id: authority.scope.request_id,
        },
        evidence: 'create_response',
      },
      signal,
    )
    if (!recorded.recorded) throw new Error('identity not recorded')
    return !late && !context.signal.aborted && Date.now() < context.deadlineMs && recorded.continue
  } catch {
    // The native container is known and no finding has been dispatched. A lost
    // factual core acknowledgment does not make that provider observation unknown.
    throw new GitHubAPIError('review_finding_failed')
  }
}
