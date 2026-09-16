import { describe, expect, it } from 'vitest'

import {
  postGitHubInlineComment,
  postGitHubTimelineComment,
  replyToGitHubReviewThread,
} from './messages'
import { operationFixture } from './operations-test-support'
import type { GitHubFinding } from './protocol'
import {
  attempt,
  comment,
  finding,
  githubFixture,
  json,
  mutationInputs,
  oldCommit,
  pr,
  thread,
} from './test-support'

describe('GitHub immediate communication primitives', () => {
  it('posts unchanged timeline text without checking or changing pending reviews', async () => {
    const fixture = await githubFixture((_request, response) => {
      json(response, { data: { addComment: { commentEdge: { node: comment } } } })
    })
    const result = await postGitHubTimelineComment(fixture.client, 7, comment.body, attempt())
    expect(mutationInputs(fixture.calls)).toEqual([
      { subjectId: 'PR_selected', body: comment.body },
    ])
    expect(result).toMatchObject({ text: comment.body, publication: 'published', id: 'IC_1' })
  })

  it.each<GitHubFinding>([
    { commit_id: oldCommit, path: 'src/main.ts', line: 12, side: 'RIGHT' },
    { commit_id: oldCommit, path: 'src/main.ts', line: 9, side: 'LEFT', subject_type: 'line' },
    {
      commit_id: oldCommit,
      path: 'src/main.ts',
      line: 12,
      side: 'RIGHT',
      start_line: 10,
      start_side: 'RIGHT',
    },
    { commit_id: oldCommit, path: 'src/main.ts', subject_type: 'file' },
  ])('publishes the exact REST location and original body: %j', async (params) => {
    const f = await operationFixture({
      native: (request, response) => {
        if (!request.query.includes('GitHubCommentIdentity')) return false
        json(response, {
          data: {
            node: {
              ...finding,
              path: params.path,
              line: params.subject_type === 'file' ? null : params.line,
              pullRequest: pr,
            },
          },
        })
        return true
      },
    })
    const result = await postGitHubInlineComment(f.client, 7, finding.body, params, attempt())
    const writes = f.calls.filter((call) => call.path.includes('/pulls/7/comments'))
    expect(writes).toEqual([
      expect.objectContaining({
        path: '/repos/new-owner/renamed/pulls/7/comments',
        body: { body: finding.body, ...params },
      }),
    ])
    expect(result).toMatchObject({ id: finding.id, publication: 'published', text: finding.body })
    expect(result.path).toBe(params.path)
    expect(result.line).toBe(params.subject_type === 'file' ? undefined : params.line)
    expect(mutationInputs(f.calls)).toEqual([])
    expect(
      f.calls.some((call) =>
        JSON.stringify(call.body ?? null).includes('query GitHubPullRequest('),
      ),
    ).toBe(false)
  })

  it('normalizes only provider SHA encoding, keeping the accepted params intact', async () => {
    const f = await operationFixture()
    const params = {
      commit_id: oldCommit.toUpperCase(),
      path: 'file',
      subject_type: 'file' as const,
    }
    await postGitHubInlineComment(f.client, 7, 'File comment', params, attempt())
    expect(f.calls.find((call) => call.path.endsWith('/comments'))?.body).toEqual({
      body: 'File comment',
      ...params,
      commit_id: oldCommit,
    })
    expect(params.commit_id).toBe(oldCommit.toUpperCase())
  })

  it('replies to the published root through REST with a lossless decimal ID', async () => {
    const f = await operationFixture()
    const result = await replyToGitHubReviewThread(
      f.client,
      7,
      thread.id,
      finding.id,
      'Reply',
      attempt(),
    )
    expect(result).toMatchObject({
      id: 'PRRC_reply',
      publication: 'published',
      replyTo: finding.id,
    })
    expect(f.calls.find((call) => call.path.endsWith('/replies'))).toMatchObject({
      path: '/repos/new-owner/renamed/pulls/7/comments/9007199254740993/replies',
      body: { body: 'Reply' },
    })
    expect(mutationInputs(f.calls)).toEqual([])
  })

  it.each(['inline', 'reply'] as const)(
    'rejects a bot draft before any %s write',
    async (action) => {
      const f = await operationFixture({ pending: true })
      const send =
        action === 'inline'
          ? postGitHubInlineComment(
              f.client,
              7,
              finding.body,
              { commit_id: oldCommit, path: 'file', subject_type: 'file' },
              attempt(),
            )
          : replyToGitHubReviewThread(f.client, 7, thread.id, finding.id, 'Reply', attempt())
      await expect(send).rejects.toMatchObject({
        code: 'pending_review_conflict',
        outcomeUnknown: false,
      })
      expect(f.events).toEqual([])
      const pending = f.calls.find((call) =>
        JSON.stringify(call.body ?? null).includes('GitHubPendingReviews'),
      )
      expect(pending?.body).toMatchObject({
        variables: { author: 'example[bot]', repository: 'R_selected', number: 7 },
      })
      expect(JSON.stringify(pending?.body)).toContain('reviews(first: 1, states: [PENDING]')
    },
  )

  it.each(['root', 'parent', 'mismatched'] as const)(
    'refuses a pending or mismatched %s thread',
    async (problem) => {
      const f = await operationFixture({
        native: (request, response) => {
          if (!request.query.includes('GitHubThreadIdentity')) return false
          json(response, {
            data: {
              node: {
                ...thread,
                comments: {
                  nodes: [
                    {
                      ...finding,
                      id: problem === 'mismatched' ? 'PRRC_other' : finding.id,
                      state: problem === 'root' ? 'PENDING' : 'SUBMITTED',
                      pullRequestReview: {
                        id: 'PRR_any',
                        state: problem === 'parent' ? 'PENDING' : 'COMMENTED',
                      },
                    },
                  ],
                },
              },
            },
          })
          return true
        },
      })
      await expect(
        replyToGitHubReviewThread(f.client, 7, thread.id, finding.id, 'Reply', attempt()),
      ).rejects.toMatchObject({ code: 'resource_unavailable', outcomeUnknown: false })
      expect(f.events).toEqual([])
    },
  )

  it.each(['comment', 'parent', 'wrong-pr', 'wrong-id', 'wrong-root', 'unavailable'] as const)(
    'never declares an accepted reply published from %s readback',
    async (problem) => {
      const f = await operationFixture({
        native: (request, response) => {
          if (!request.query.includes('GitHubCommentIdentity')) return false
          if (problem === 'unavailable') json(response, {}, 503)
          else
            json(response, {
              data: {
                node: {
                  ...finding,
                  id: problem === 'wrong-id' ? 'PRRC_other' : 'PRRC_reply',
                  state: problem === 'comment' ? 'PENDING' : 'SUBMITTED',
                  pullRequestReview: {
                    id: 'PRR_any',
                    state: problem === 'parent' ? 'PENDING' : 'COMMENTED',
                  },
                  replyTo: { id: problem === 'wrong-root' ? 'PRRC_other' : finding.id },
                  pullRequest: problem === 'wrong-pr' ? { ...pr, number: 8 } : pr,
                },
              },
            })
          return true
        },
      })
      await expect(
        replyToGitHubReviewThread(f.client, 7, thread.id, finding.id, 'Reply', attempt()),
      ).rejects.toMatchObject({
        code: 'send_outcome_unknown',
        outcomeUnknown: true,
        retryable: false,
      })
      expect(f.events).toEqual(['reply'])
    },
  )

  it('rejects a concurrent native pending conflict without a GraphQL fallback', async () => {
    const f = await operationFixture({
      rest: (_call, response) => {
        json(response, { message: 'private pending review conflict' }, 422)
        return true
      },
    })
    await expect(
      replyToGitHubReviewThread(f.client, 7, thread.id, finding.id, 'Reply', attempt()),
    ).rejects.toMatchObject({ outcomeUnknown: false, retryable: false })
    expect(f.events).toEqual(['reply'])
    expect(mutationInputs(f.calls)).toEqual([])
  })
})
