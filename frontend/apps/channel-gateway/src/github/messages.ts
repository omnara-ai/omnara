import { z } from 'zod'

import type { OperationAttemptContext } from '../operation-retry'
import type { GitHubClient } from './client'
import type { GitHubFindingInput, GitHubThreadReplyInput } from './protocol'
import {
  GitHubAPIError,
  type GitHubComment,
  githubComment,
  githubCommitID,
  githubFileFinding,
  type GitHubFinding,
  githubLineFinding,
  githubNodeID,
  githubPRIdentity,
  githubPRNumber,
  type GitHubReview,
  githubReview,
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
  publication: 'draft' | 'published'
  reviewID?: string
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
    publication: 'state' in value && value.state === 'PENDING' ? 'draft' : 'published',
  }
  if ('submittedAt' in value) {
    message.reviewID = value.id
    message.commitID = value.commit?.oid
  }
  if ('pullRequestReview' in value) {
    message.reviewID = value.pullRequestReview?.id
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

/** No orchestration: this method creates only the container. The caller must record
 * its returned ID durably and receive continuation authority before addFinding.
 */
export async function createGitHubPendingReview(
  client: GitHubClient,
  number: number,
  commitID: string,
  marker: string,
  context: OperationAttemptContext,
): Promise<{ id: string }> {
  providerValue(githubCommitID, commitID)
  validateGitHubText(marker)
  const pr = await getGitHubPullRequest(client, number, context)
  const data = await client.query(
    'createReview',
    {
      input: { pullRequestId: pr.id, commitOID: commitID, body: marker },
    },
    z.object({
      addPullRequestReview: z.object({ pullRequestReview: z.object({ id: githubNodeID }) }),
    }),
    context,
  )
  return data.addPullRequestReview.pullRequestReview
}

export async function postGitHubTimelineComment(
  client: GitHubClient,
  number: number,
  text: string,
  context: OperationAttemptContext,
): Promise<GitHubMessage> {
  validateGitHubText(text)
  const pr = await getGitHubPullRequest(client, number, context)
  const data = await client.query(
    'timelineComment',
    { input: { subjectId: pr.id, body: text } },
    z.object({
      addComment: z.object({ commentEdge: z.object({ node: githubComment }) }),
    }),
    context,
  )
  return githubMessage(data.addComment.commentEdge.node, true)
}

export async function addGitHubReviewFinding(
  client: GitHubClient,
  number: number,
  reviewID: string,
  text: string,
  params: GitHubFinding,
  context: OperationAttemptContext,
) {
  providerValue(githubNodeID, reviewID)
  providerValue(z.union([githubLineFinding, githubFileFinding]), params)
  validateGitHubText(text)
  if (params.review_id !== undefined && params.review_id !== reviewID)
    throw new GitHubAPIError('review_not_owned')
  await getGitHubPullRequest(client, number, context)
  // FILE has no invented line/side. Both branches explicitly target the recorded review.
  const input: GitHubFindingInput = {
    pullRequestReviewId: reviewID,
    body: text,
    path: params.path,
    subjectType: params.subject_type === 'file' ? 'FILE' : 'LINE',
  }
  if (params.subject_type !== 'file') {
    input.line = params.line
    input.side = params.side
    if (params.start_line !== undefined) input.startLine = params.start_line
    if (params.start_side !== undefined) input.startSide = params.start_side
  }
  const data = await client.query(
    'addFinding',
    {
      input,
    },
    z.object({ addPullRequestReviewThread: z.object({ thread: threadIdentity }) }),
    context,
  )
  const thread = data.addPullRequestReviewThread.thread
  assertGitHubPRScope(client, number, thread.pullRequest, true)
  const comment = thread.comments.nodes[0]
  if (!comment || comment.pullRequestReview?.id !== reviewID || comment.state !== 'PENDING')
    throw new GitHubAPIError('invalid_review_response', { outcomeUnknown: true })
  return { threadID: thread.id, message: githubMessage(comment, true) }
}

export async function submitGitHubReview(
  client: GitHubClient,
  number: number,
  reviewID: string,
  summary: string,
  context: OperationAttemptContext,
): Promise<GitHubMessage> {
  providerValue(githubNodeID, reviewID)
  validateGitHubText(summary)
  await getGitHubPullRequest(client, number, context)
  const data = await client.query(
    'submitReview',
    {
      input: { pullRequestReviewId: reviewID, event: 'COMMENT', body: summary },
    },
    z.object({ submitPullRequestReview: z.object({ pullRequestReview: githubReview }) }),
    context,
  )
  const review = data.submitPullRequestReview.pullRequestReview
  if (review.id !== reviewID || review.state !== 'COMMENTED')
    throw new GitHubAPIError('invalid_review_response', { outcomeUnknown: true })
  return githubMessage(review, true)
}

/** Caller must first run the bounded pending guard; this does not create a local draft. */
export async function postGitHubReviewSummary(
  client: GitHubClient,
  number: number,
  summary: string,
  context: OperationAttemptContext,
): Promise<GitHubMessage> {
  validateGitHubText(summary)
  const pr = await getGitHubPullRequest(client, number, context)
  const data = await client.query(
    'reviewSummary',
    { input: { pullRequestId: pr.id, event: 'COMMENT', body: summary } },
    z.object({ addPullRequestReview: z.object({ pullRequestReview: githubReview }) }),
    context,
  )
  if (data.addPullRequestReview.pullRequestReview.state !== 'COMMENTED')
    throw new GitHubAPIError('invalid_review_response', { outcomeUnknown: true })
  return githubMessage(data.addPullRequestReview.pullRequestReview, true)
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

export async function getGitHubReview(
  client: GitHubClient,
  number: number,
  reviewID: string,
  context: OperationAttemptContext,
) {
  providerValue(githubNodeID, reviewID)
  const data = await client.query(
    'reviewIdentity',
    { review: reviewID },
    z.object({ node: githubReview.extend({ pullRequest: githubPRIdentity }).nullable() }),
    context,
  )
  if (!data.node) throw new GitHubAPIError('review_state_unavailable')
  assertGitHubPRScope(client, number, data.node.pullRequest)
  if (data.node.id !== reviewID) throw new GitHubAPIError('invalid_review_response')
  return data.node
}

/** Drafts require core-proven ownership. Published threads use REST; an existing
 * native pending review can reject that request, with no mutation fallback.
 */
export async function replyToGitHubReviewThread(
  client: GitHubClient,
  number: number,
  threadID: string,
  text: string,
  context: OperationAttemptContext,
  ownedReviewID?: string,
): Promise<GitHubMessage> {
  validateGitHubText(text)
  if (ownedReviewID !== undefined) providerValue(githubNodeID, ownedReviewID)
  const thread = await getGitHubReviewThread(client, number, threadID, context)
  const root = thread.comments.nodes[0]
  if (root?.state === 'PENDING' && (!ownedReviewID || ownedReviewID !== root.pullRequestReview?.id))
    throw new GitHubAPIError('review_not_owned')
  if (root?.state === 'SUBMITTED') {
    if (!root.fullDatabaseId) throw new GitHubAPIError('resource_unavailable')
    const id = await client.publishedReply(number, root.fullDatabaseId, text, context)
    // REST exposes node_id but not publication state. Inspect the created comment;
    // failure of this read must never repeat the already-accepted REST mutation.
    try {
      const result = await client.query(
        'commentIdentity',
        { comment: id },
        z.object({
          node: githubReviewComment.extend({ pullRequest: githubPRIdentity }).nullable(),
        }),
        context,
      )
      const value = result.node
      if (
        !value ||
        value.id !== id ||
        value.state !== 'SUBMITTED' ||
        value.pullRequestReview?.state === 'PENDING' ||
        value.replyTo?.id !== root.id
      )
        throw new GitHubAPIError('invalid_review_response')
      assertGitHubPRScope(client, number, value.pullRequest)
      return githubMessage(value, true)
    } catch {
      throw new GitHubAPIError('reply_outcome_unknown', { outcomeUnknown: true })
    }
  }
  const input: GitHubThreadReplyInput = { pullRequestReviewThreadId: threadID, body: text }
  if (ownedReviewID !== undefined) input.pullRequestReviewId = ownedReviewID
  const data = await client.query(
    'threadReply',
    { input },
    z.object({ addPullRequestReviewThreadReply: z.object({ comment: githubReviewComment }) }),
    context,
  )
  const reply = data.addPullRequestReviewThreadReply.comment
  if (reply.pullRequestReview?.id !== ownedReviewID)
    throw new GitHubAPIError('invalid_review_response', { outcomeUnknown: true })
  return githubMessage(reply, true)
}
