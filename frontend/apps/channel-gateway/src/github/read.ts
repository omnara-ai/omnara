import { z } from 'zod'

import {
  type OperationAttemptContext,
  type OperationRetryOptions,
  retryOperation,
} from '../operation-retry'
import { parseObjectFields } from '../operations-json'
import type { GitHubClient } from './client'
import { assertGitHubPRScope, type GitHubMessage, githubMessage } from './messages'
import {
  GitHubAPIError,
  githubComment,
  githubNodeID,
  githubPageInfo,
  githubPRIdentity,
  githubPRNumber,
  githubReview,
  githubReviewComment,
  providerValue,
} from './protocol'

export interface GitHubReadInput {
  number: number
  threadID?: string
  limit: number
  /** Core separately binds the public cursor to agent/channel authorization. */
  cursor?: string
}
export interface GitHubHistoryPage {
  messages: GitHubMessage[]
  nextCursor?: string
  coverage: 'complete' | 'partial'
  reason?: string
}
const cursorSchema = z.strictObject({
  repository: githubNodeID,
  number: githubPRNumber,
  thread: githubNodeID.nullable(),
  before: z.string().min(1).max(2048),
})
const timelineItem = z.discriminatedUnion('__typename', [
  githubComment.extend({ __typename: z.literal('IssueComment') }),
  githubReview.extend({ __typename: z.literal('PullRequestReview') }),
])

/** Native last/before pagination returns a chronological page, newest page first.
 * PR history covers comments/review summaries; inline discussion is on child threads.
 * No REST Link URL, diff, source contents, or unbounded history scan is followed.
 */
export async function readGitHubHistory(
  client: GitHubClient,
  input: GitHubReadInput,
  operation: Omit<OperationRetryOptions, 'idempotent'>,
): Promise<GitHubHistoryPage> {
  validateHistoryInput(client, input)
  return retryOperation({ ...operation, idempotent: true }, (context) =>
    readGitHubHistoryAttempt(client, input, context),
  )
}

/** One page inside a caller's existing aggregate retry budget. */
export async function readGitHubHistoryAttempt(
  client: GitHubClient,
  input: GitHubReadInput,
  context: OperationAttemptContext,
): Promise<GitHubHistoryPage> {
  const before = validateHistoryInput(client, input)
  const page = input.threadID
    ? await threadPage(client, input, context, before)
    : await timelinePage(client, input, context, before)
  if (page.nodes.length > input.limit) throw new GitHubAPIError('invalid_history_response')
  const { hasPreviousPage, startCursor } = page.pageInfo
  if (hasPreviousPage && (!startCursor || startCursor === before || !page.nodes.length))
    throw new GitHubAPIError('nonadvancing_history')
  const messages = page.nodes
    .filter((node) => node.body.length > 0)
    .map((node) => githubMessage(node))
  if (Buffer.byteLength(JSON.stringify(messages)) > 1024 * 1024)
    throw new GitHubAPIError('history_too_large')
  const omittedEmpty = messages.length !== page.nodes.length
  const partial = !input.threadID || omittedEmpty
  return {
    messages,
    coverage: partial ? 'partial' : 'complete',
    reason: !input.threadID
      ? 'timeline_excludes_inline_discussion_and_non_comment_events'
      : omittedEmpty
        ? 'empty_comment_bodies_omitted'
        : undefined,
    nextCursor: hasPreviousPage
      ? Buffer.from(
          JSON.stringify({
            repository: client.configuration.repositoryNodeID,
            number: input.number,
            thread: input.threadID ?? null,
            before: startCursor,
          }),
        ).toString('base64url')
      : undefined,
  }
}

async function timelinePage(
  client: GitHubClient,
  input: GitHubReadInput,
  context: OperationAttemptContext,
  before?: string,
) {
  const data = await client.query(
    'timeline',
    {
      repository: client.configuration.repositoryNodeID,
      number: input.number,
      limit: input.limit,
      before,
    },
    z.object({
      node: z
        .object({
          id: githubNodeID,
          pullRequest: githubPRIdentity
            .extend({
              timelineItems: z.object({
                nodes: z.array(timelineItem).max(100),
                pageInfo: githubPageInfo,
              }),
            })
            .nullable(),
        })
        .nullable(),
    }),
    context,
  )
  const pr = data.node?.pullRequest
  if (!pr) throw new GitHubAPIError('resource_unavailable')
  assertGitHubPRScope(client, input.number, pr)
  if (data.node?.id !== client.configuration.repositoryNodeID)
    throw new GitHubAPIError('repository_scope_mismatch')
  return pr.timelineItems
}

async function threadPage(
  client: GitHubClient,
  input: GitHubReadInput,
  context: OperationAttemptContext,
  before?: string,
) {
  const data = await client.query(
    'thread',
    { thread: input.threadID, limit: input.limit, before },
    z.object({
      node: z
        .object({
          id: githubNodeID,
          pullRequest: githubPRIdentity,
          comments: z.object({
            nodes: z.array(githubReviewComment).max(100),
            pageInfo: githubPageInfo,
          }),
        })
        .nullable(),
    }),
    context,
  )
  if (!data.node) throw new GitHubAPIError('resource_unavailable')
  assertGitHubPRScope(client, input.number, data.node.pullRequest)
  if (data.node.id !== input.threadID) throw new GitHubAPIError('invalid_history_response')
  return data.node.comments
}

function decodeCursor(client: GitHubClient, input: GitHubReadInput): string | undefined {
  if (input.cursor === undefined) return undefined
  try {
    if (!input.cursor || input.cursor.length > 4096 || !/^[\w-]+$/.test(input.cursor))
      throw new Error('cursor')
    const raw = new TextDecoder('utf-8', { fatal: true }).decode(
      Buffer.from(input.cursor, 'base64url'),
    )
    parseObjectFields(raw, 4096)
    const cursor = cursorSchema.parse(JSON.parse(raw))
    if (
      cursor.repository !== client.configuration.repositoryNodeID ||
      cursor.number !== input.number ||
      cursor.thread !== (input.threadID ?? null)
    )
      throw new Error('cursor scope')
    return cursor.before
  } catch {
    throw new GitHubAPIError('invalid_history_cursor')
  }
}

/** Facts only: core maps each native ID/marker to a creator. A truncated listing
 * cannot authorize an omitted-ID create/summary, and absence is not deletion proof.
 */
export async function listGitHubPendingReviews(
  client: GitHubClient,
  number: number,
  context: OperationAttemptContext,
) {
  providerValue(githubPRNumber, number)
  const { viewer } = await client.query(
    'viewer',
    {},
    z.object({
      viewer: z.object({ login: z.string().min(1).max(256) }),
    }),
    context,
  )
  const data = await client.query(
    'pendingReviews',
    {
      repository: client.configuration.repositoryNodeID,
      number,
      author: viewer.login,
    },
    z.object({
      node: z
        .object({
          id: githubNodeID,
          pullRequest: githubPRIdentity
            .extend({
              reviews: z
                .object({
                  nodes: z.array(githubReview).max(100),
                  pageInfo: z.object({
                    hasNextPage: z.boolean(),
                    endCursor: z.string().max(2048).nullable(),
                  }),
                })
                .nullable(),
            })
            .nullable(),
        })
        .nullable(),
    }),
    context,
  )
  const pr = data.node?.pullRequest
  if (!pr?.reviews) throw new GitHubAPIError('review_state_unavailable')
  assertGitHubPRScope(client, number, pr)
  if (
    data.node?.id !== client.configuration.repositoryNodeID ||
    pr.reviews.nodes.some(
      (review) => review.state !== 'PENDING' || review.author?.login !== viewer.login,
    )
  )
    throw new GitHubAPIError('invalid_review_response')
  return { reviews: pr.reviews.nodes, complete: !pr.reviews.pageInfo.hasNextPage }
}

function validateHistoryInput(client: GitHubClient, input: GitHubReadInput): string | undefined {
  providerValue(githubPRNumber, input.number)
  if (!Number.isInteger(input.limit) || input.limit < 1 || input.limit > 100)
    throw new GitHubAPIError('invalid_history_limit')
  if (input.threadID !== undefined) providerValue(githubNodeID, input.threadID)
  return decodeCursor(client, input)
}
