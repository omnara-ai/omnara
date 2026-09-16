import { z } from 'zod'

import type { OperationAttemptContext } from '../operation-retry'
import type { GitHubClient } from './client'
import { assertGitHubPRScope } from './messages'
import { GitHubAPIError, githubNodeID, githubPRIdentity, githubReview } from './protocol'

/** Read only current communication identity. The signed saved body remains the
 * input text; fetching a mutable comment cannot replace accepted communication.
 * Only published native children may be attached by the inbound behavior.
 */
export async function githubInboundThread(
  client: GitHubClient,
  number: number,
  commentID: string,
  context: OperationAttemptContext,
): Promise<{ rootID: string; threadID: string } | undefined> {
  const data = await client.query(
    'inboundComment',
    { comment: commentID },
    z.object({
      node: z
        .object({
          id: githubNodeID,
          state: z.enum(['PENDING', 'SUBMITTED']),
          replyTo: z.object({ id: githubNodeID }).nullable(),
          pullRequestReview: z
            .object({ id: githubNodeID, state: githubReview.shape.state })
            .nullable(),
          pullRequest: githubPRIdentity,
        })
        .nullable(),
    }),
    context,
  )
  if (!data.node) return undefined
  if (data.node.id !== commentID) throw new GitHubAPIError('comment_scope_mismatch')
  assertGitHubPRScope(client, number, data.node.pullRequest)
  if (data.node.state !== 'SUBMITTED' || data.node.pullRequestReview?.state === 'PENDING')
    throw new GitHubAPIError('private_inbound_comment')
  const rootID = data.node.replyTo?.id ?? data.node.id
  let after: string | undefined
  const seen = new Set<string>()
  for (let page = 0; page < 100; page += 1) {
    const result = await client.query(
      'inboundThreads',
      {
        repository: client.configuration.repositoryNodeID,
        number,
        after,
      },
      z.object({
        node: z
          .object({
            id: githubNodeID,
            pullRequest: githubPRIdentity
              .extend({
                reviewThreads: z.object({
                  nodes: z
                    .array(
                      z.object({
                        id: githubNodeID,
                        comments: z.object({
                          nodes: z
                            .array(
                              z.object({
                                id: githubNodeID,
                                state: z.enum(['PENDING', 'SUBMITTED']),
                              }),
                            )
                            .max(1),
                        }),
                      }),
                    )
                    .max(100),
                  pageInfo: z.object({
                    hasNextPage: z.boolean(),
                    endCursor: z.string().min(1).max(2048).nullable(),
                  }),
                }),
              })
              .nullable(),
          })
          .nullable(),
      }),
      context,
    )
    const pr = result.node?.pullRequest
    if (!pr || result.node?.id !== client.configuration.repositoryNodeID)
      throw new GitHubAPIError('repository_scope_mismatch')
    assertGitHubPRScope(client, number, pr)
    const match = pr.reviewThreads.nodes.find((thread) => thread.comments.nodes[0]?.id === rootID)
    if (match) {
      if (match.comments.nodes[0]?.state !== 'SUBMITTED')
        throw new GitHubAPIError('private_inbound_comment')
      return { rootID, threadID: match.id }
    }
    const next = pr.reviewThreads.pageInfo
    if (!next.hasNextPage) throw new GitHubAPIError('thread_identity_unavailable')
    if (!next.endCursor || seen.has(next.endCursor) || !pr.reviewThreads.nodes.length)
      throw new GitHubAPIError('invalid_thread_page')
    seen.add(next.endCursor)
    after = next.endCursor
  }
  throw new GitHubAPIError('thread_observation_incomplete')
}
