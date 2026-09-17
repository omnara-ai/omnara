import { z } from 'zod'

import type { OperationAttemptContext } from '../operations/retry'
import type { GitHubClient } from './client'
import {
  GitHubAPIError,
  type GitHubComment,
  githubCommitID,
  type GitHubFinding,
  githubNodeID,
  githubPRIdentity,
  githubPRNumber,
  type GitHubReview,
  type GitHubReviewComment,
  githubReviewComment,
  providerValue,
  validateGitHubText,
} from './protocol'

export interface GitHubMessage {
  id: string
  text: string
  authorRef?: string
  createdAt: string
  publication: 'published'
  commitID?: string
  replyTo?: string
  path?: string
  line?: number
}

/** Bodies are already native Markdown. Preserve them, including code and mentions. */
export function githubMessage(
  value: GitHubComment | GitHubReview | GitHubReviewComment,
  mutation = false,
): GitHubMessage {
  if (Buffer.byteLength(value.body) > 64 * 1024)
    throw new GitHubAPIError('message_too_large', { outcomeUnknown: mutation })
  if (value.body.includes('\u0000') || Buffer.from(value.body).toString('utf8') !== value.body)
    throw new GitHubAPIError('invalid_message', { outcomeUnknown: mutation })
  const message: GitHubMessage = {
    id: value.id,
    text: value.body,
    createdAt: value.createdAt,
    authorRef: value.author?.login,
    publication: 'published',
  }
  if ('submittedAt' in value) {
    message.commitID = value.commit?.oid
  }
  if ('pullRequestReview' in value) {
    message.commitID = value.originalCommit?.oid
    message.replyTo = value.replyTo?.id
    message.path = value.path
    message.line = value.line ?? undefined
  }
  return message
}

export function assertGitHubPRScope(
  client: GitHubClient,
  number: number,
  identity: z.infer<typeof githubPRIdentity>,
  mutation = false,
): void {
  if (
    identity.number !== number ||
    identity.repository.id !== client.configuration.repositoryNodeID
  )
    throw new GitHubAPIError('repository_scope_mismatch', { outcomeUnknown: mutation })
}

export async function getGitHubPullRequest(
  client: GitHubClient,
  number: number,
  context: OperationAttemptContext,
) {
  providerValue(githubPRNumber, number)
  const data = await client.query(
    'pullRequest',
    { repository: client.configuration.repositoryNodeID, number },
    z.object({
      node: z
        .object({
          id: githubNodeID,
          pullRequest: githubPRIdentity
            .extend({
              title: z.string(),
              state: z.enum(['OPEN', 'CLOSED', 'MERGED']),
              headRefOid: githubCommitID,
            })
            .nullable(),
        })
        .nullable(),
    }),
    context,
  )
  const pr = data.node?.pullRequest
  if (!pr) throw new GitHubAPIError('resource_unavailable')
  assertGitHubPRScope(client, number, pr)
  if (data.node?.id !== client.configuration.repositoryNodeID)
    throw new GitHubAPIError('repository_scope_mismatch')
  return pr
}

export async function postGitHubTimelineComment(
  client: GitHubClient,
  number: number,
  text: string,
  context: OperationAttemptContext,
): Promise<GitHubMessage> {
  validateGitHubText(text)
  await getGitHubPullRequest(client, number, context)
  return githubMessage(await client.publishedTimeline(number, text, context), true)
}

const threadIdentity = z.object({
  id: githubNodeID,
  pullRequest: githubPRIdentity,
  comments: z.object({ nodes: z.array(githubReviewComment).length(1) }),
})

export async function getGitHubReviewThread(
  client: GitHubClient,
  number: number,
  threadID: string,
  context: OperationAttemptContext,
) {
  providerValue(githubPRNumber, number)
  providerValue(githubNodeID, threadID)
  const data = await client.query(
    'threadIdentity',
    { thread: threadID },
    z.object({ node: threadIdentity.nullable() }),
    context,
  )
  if (!data.node) throw new GitHubAPIError('resource_unavailable')
  assertGitHubPRScope(client, number, data.node.pullRequest)
  if (data.node.id !== threadID) throw new GitHubAPIError('invalid_response')
  return data.node
}

/** Refuse any current draft belonging to this shared native bot. There is no
 * local ownership, adoption, submission or deletion. A concurrent native conflict
 * is still handled by the REST response; no GraphQL mutation fallback exists.
 * GitHub documents no bypass guarantee for an existing bot draft. This bounded
 * preflight is conservative, not atomic isolation; publication is read back too.
 */
async function requireNoPendingReview(
  client: GitHubClient,
  number: number,
  context: OperationAttemptContext,
): Promise<void> {
  providerValue(githubPRNumber, number)
  const login = await client.viewerLogin(context)
  const result = await client.query(
    'pendingReviews',
    { repository: client.configuration.repositoryNodeID, number, author: login },
    z.object({
      node: z
        .object({
          id: githubNodeID,
          pullRequest: githubPRIdentity
            .extend({
              reviews: z
                .object({ nodes: z.array(z.object({ id: githubNodeID })).max(1) })
                .nullable(),
            })
            .nullable(),
        })
        .nullable(),
    }),
    context,
  )
  const pr = result.node?.pullRequest
  if (!pr) throw new GitHubAPIError('resource_unavailable')
  if (!pr.reviews) throw new GitHubAPIError('provider_unavailable')
  assertGitHubPRScope(client, number, pr)
  if (result.node?.id !== client.configuration.repositoryNodeID)
    throw new GitHubAPIError('repository_scope_mismatch')
  if (pr.reviews.nodes.length) throw new GitHubAPIError('pending_review_conflict')
}

/** REST returns a comment identity but no publication state. An accepted write
 * followed by an unavailable/contradictory read is unknown, never a safe resend.
 */
async function publishedCommentResult(
  client: GitHubClient,
  number: number,
  id: string,
  rootID: string | undefined,
  context: OperationAttemptContext,
): Promise<GitHubMessage> {
  try {
    const result = await client.query(
      'commentIdentity',
      { comment: id },
      z.object({ node: githubReviewComment.extend({ pullRequest: githubPRIdentity }).nullable() }),
      context,
    )
    const value = result.node
    if (
      !value ||
      value.id !== id ||
      value.state !== 'SUBMITTED' ||
      value.pullRequestReview?.state === 'PENDING' ||
      value.replyTo?.id !== rootID
    )
      throw new GitHubAPIError('invalid_response')
    assertGitHubPRScope(client, number, value.pullRequest)
    return githubMessage(value, true)
  } catch {
    throw new GitHubAPIError('send_outcome_unknown', { outcomeUnknown: true })
  }
}

export async function postGitHubInlineComment(
  client: GitHubClient,
  number: number,
  text: string,
  params: GitHubFinding,
  context: OperationAttemptContext,
): Promise<GitHubMessage> {
  validateGitHubText(text)
  await requireNoPendingReview(client, number, context)
  const id = await client.publishedComment(number, text, params, context)
  return publishedCommentResult(client, number, id, undefined, context)
}

export async function replyToGitHubReviewThread(
  client: GitHubClient,
  number: number,
  threadID: string,
  rootID: string,
  text: string,
  context: OperationAttemptContext,
): Promise<GitHubMessage> {
  validateGitHubText(text)
  const thread = await getGitHubReviewThread(client, number, threadID, context)
  const root = thread.comments.nodes[0]
  if (
    !root ||
    root.id !== rootID ||
    root.state !== 'SUBMITTED' ||
    root.pullRequestReview?.state === 'PENDING' ||
    !root.fullDatabaseId
  )
    throw new GitHubAPIError('resource_unavailable')
  await requireNoPendingReview(client, number, context)
  const id = await client.publishedReply(number, root.fullDatabaseId, text, context)
  return publishedCommentResult(client, number, id, root.id, context)
}
