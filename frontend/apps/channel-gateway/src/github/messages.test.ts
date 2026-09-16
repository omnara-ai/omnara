import { describe, expect, it } from 'vitest'

import {
  addGitHubReviewFinding,
  createGitHubPendingReview,
  postGitHubReviewSummary,
  postGitHubTimelineComment,
  replyToGitHubReviewThread,
  submitGitHubReview,
} from './messages'
import {
  attempt,
  comment,
  finding,
  githubFixture,
  json,
  mutationInputs,
  oldCommit,
  pr,
  review,
  thread,
} from './test-support'

describe('GitHub communication primitives', () => {
  it('requires only the native identity when creating a pending container', async () => {
    const { client } = await githubFixture((_request, response) => {
      json(response, {
        data: { addPullRequestReview: { pullRequestReview: { id: 'PRR_1' } } },
      })
    })
    const result = await createGitHubPendingReview(
      client,
      7,
      oldCommit,
      '<!-- omnara-review:tc_test -->',
      attempt(),
    )
    expect(result).toEqual({ id: 'PRR_1' })
  })
  it('posts one unchanged timeline message, with no review side effect', async () => {
    const fixture = await githubFixture((_request, response) => {
      json(response, { data: { addComment: { commentEdge: { node: comment } } } })
    })
    const result = await postGitHubTimelineComment(fixture.client, 7, comment.body, attempt())
    expect(mutationInputs(fixture.calls)).toEqual([
      { subjectId: 'PR_selected', body: comment.body },
    ])
    expect(result).toMatchObject({ text: comment.body, publication: 'published', id: 'IC_1' })
  })

  it.each(['line', 'file'] as const)(
    'creates only a container; a separate explicit call adds one %s finding to its recorded ID',
    async (subjectType) => {
      const fixture = await githubFixture((request, response) => {
        if (request.query.startsWith('mutation GitHubCreateReview'))
          json(response, { data: { addPullRequestReview: { pullRequestReview: review } } })
        else json(response, { data: { addPullRequestReviewThread: { thread } } })
      })
      const created = await createGitHubPendingReview(
        fixture.client,
        7,
        oldCommit,
        '<!-- omnara-review:tc_test -->',
        attempt(),
      )
      expect(mutationInputs(fixture.calls)).toEqual([
        {
          pullRequestId: 'PR_selected',
          commitOID: oldCommit,
          body: '<!-- omnara-review:tc_test -->',
        },
      ])
      const params =
        subjectType === 'file'
          ? {
              review_comment: true as const,
              commit_id: oldCommit,
              path: 'src/main.ts',
              subject_type: 'file' as const,
            }
          : {
              review_comment: true as const,
              commit_id: oldCommit,
              path: 'src/main.ts',
              line: 12,
              side: 'RIGHT' as const,
            }
      const result = await addGitHubReviewFinding(
        fixture.client,
        7,
        created.id,
        finding.body,
        params,
        attempt(),
      )
      expect(mutationInputs(fixture.calls)[1]).toMatchObject({
        pullRequestReviewId: 'PRR_1',
        body: finding.body,
        path: 'src/main.ts',
        subjectType: subjectType.toUpperCase(),
      })
      if (subjectType === 'file') {
        expect(mutationInputs(fixture.calls)[1]).not.toHaveProperty('line')
        expect(mutationInputs(fixture.calls)[1]).not.toHaveProperty('side')
      }
      expect(result).toMatchObject({
        threadID: 'PRRT_1',
        message: { publication: 'draft', reviewID: 'PRR_1', commitID: oldCommit },
      })
    },
  )

  it('submits the exact owned old-commit review as COMMENT even when PR metadata is closed/newer', async () => {
    const fixture = await githubFixture((_request, response) => {
      json(response, {
        data: {
          submitPullRequestReview: {
            pullRequestReview: {
              ...review,
              state: 'COMMENTED',
              body: 'Summary',
              submittedAt: '2026-09-15T13:00:00Z',
            },
          },
        },
      })
    })
    const result = await submitGitHubReview(fixture.client, 7, 'PRR_1', 'Summary', attempt())
    expect(mutationInputs(fixture.calls)).toEqual([
      { pullRequestReviewId: 'PRR_1', event: 'COMMENT', body: 'Summary' },
    ])
    expect(result).toMatchObject({
      publication: 'published',
      commitID: oldCommit,
      reviewID: 'PRR_1',
    })
  })

  it('summary-only submission uses the native COMMENT path without a pending container', async () => {
    const fixture = await githubFixture((_request, response) => {
      json(response, {
        data: {
          addPullRequestReview: {
            pullRequestReview: {
              ...review,
              state: 'COMMENTED',
              body: 'Summary',
              submittedAt: '2026-09-15T13:00:00Z',
            },
          },
        },
      })
    })
    await postGitHubReviewSummary(fixture.client, 7, 'Summary', attempt())
    expect(mutationInputs(fixture.calls)).toEqual([
      { pullRequestId: 'PR_selected', event: 'COMMENT', body: 'Summary' },
    ])
  })

  it('requires the explicit owned ID for a draft reply and forwards it to GitHub', async () => {
    const fixture = await githubFixture((request, response) => {
      if (request.query.startsWith('query ')) json(response, { data: { node: thread } })
      else
        json(response, {
          data: {
            addPullRequestReviewThreadReply: {
              comment: { ...finding, id: 'PRRC_reply', replyTo: { id: 'PRRC_1' } },
            },
          },
        })
    })
    await expect(
      replyToGitHubReviewThread(fixture.client, 7, thread.id, 'Reply', attempt()),
    ).rejects.toMatchObject({ code: 'review_not_owned' })
    expect(mutationInputs(fixture.calls)).toHaveLength(0)
    const result = await replyToGitHubReviewThread(
      fixture.client,
      7,
      thread.id,
      'Reply',
      attempt(),
      'PRR_1',
    )
    expect(mutationInputs(fixture.calls)).toEqual([
      { pullRequestReviewThreadId: 'PRRT_1', pullRequestReviewId: 'PRR_1', body: 'Reply' },
    ])
    expect(result).toMatchObject({ publication: 'draft', replyTo: 'PRRC_1', reviewID: 'PRR_1' })
  })

  it('uses the lossless root ID and REST for a published thread, without a GraphQL mutation', async () => {
    const fixture = await githubFixture(
      (request, response) => {
        if (request.query.includes('GitHubThreadIdentity'))
          json(response, { data: { node: publishedThread() } })
        else
          json(response, {
            data: {
              node: {
                ...finding,
                id: 'PRRC_reply',
                state: 'SUBMITTED',
                pullRequest: pr,
                replyTo: { id: finding.id },
                pullRequestReview: { id: 'PRR_reply', state: 'COMMENTED' },
              },
            },
          })
      },
      (call, response) => {
        expect(call.path).toBe('/repos/new-owner/renamed/pulls/7/comments/9007199254740993/replies')
        expect(call.body).toEqual({ body: 'Reply' })
        json(response, { node_id: 'PRRC_reply' }, 201)
      },
    )
    const result = await replyToGitHubReviewThread(fixture.client, 7, thread.id, 'Reply', attempt())
    expect(result).toMatchObject({
      id: 'PRRC_reply',
      publication: 'published',
      reviewID: 'PRR_reply',
    })
    expect(mutationInputs(fixture.calls)).toEqual([])
  })

  it('does not fall back to GraphQL when REST rejects a foreign pending review', async () => {
    const fixture = await githubFixture(
      (_request, response) => {
        json(response, { data: { node: publishedThread() } })
      },
      (_call, response) => {
        json(
          response,
          { message: 'user_id can only have one pending review per pull request' },
          422,
        )
      },
    )
    await expect(
      replyToGitHubReviewThread(fixture.client, 7, thread.id, 'Reply', attempt()),
    ).rejects.toMatchObject({ outcomeUnknown: false })
    expect(mutationInputs(fixture.calls)).toEqual([])
    expect(fixture.calls.filter((call) => call.path.endsWith('/replies'))).toHaveLength(1)
  })

  it('never reports REST reply success if native readback puts it in a pending review', async () => {
    const fixture = await githubFixture(
      (request, response) => {
        if (request.query.includes('GitHubThreadIdentity'))
          json(response, { data: { node: publishedThread() } })
        else
          json(response, {
            data: {
              node: { ...finding, id: 'PRRC_reply', pullRequest: pr, replyTo: { id: finding.id } },
            },
          })
      },
      (_call, response) => {
        json(response, { node_id: 'PRRC_reply' }, 201)
      },
    )
    await expect(
      replyToGitHubReviewThread(fixture.client, 7, thread.id, 'Reply', attempt()),
    ).rejects.toMatchObject({ outcomeUnknown: true })
    expect(mutationInputs(fixture.calls)).toEqual([])
    expect(fixture.calls.filter((call) => call.path.endsWith('/replies'))).toHaveLength(1)
  })

  it('reports unknown after a known REST creation whose publication read fails', async () => {
    const fixture = await githubFixture(
      (request, response) => {
        if (request.query.includes('GitHubThreadIdentity'))
          json(response, { data: { node: publishedThread() } })
        else json(response, {}, 500)
      },
      (_call, response) => {
        json(response, { node_id: 'PRRC_reply' }, 201)
      },
    )
    await expect(
      replyToGitHubReviewThread(fixture.client, 7, thread.id, 'Reply', attempt()),
    ).rejects.toMatchObject({ outcomeUnknown: true, retryable: false })
    expect(mutationInputs(fixture.calls)).toEqual([])
    expect(fixture.calls.filter((call) => call.path.endsWith('/replies'))).toHaveLength(1)
  })

  it('rejects missing numeric root identity before REST dispatch', async () => {
    const fixture = await githubFixture((_request, response) => {
      json(response, {
        data: {
          node: {
            ...publishedThread(),
            comments: { nodes: [{ ...finding, state: 'SUBMITTED', fullDatabaseId: null }] },
          },
        },
      })
    })
    await expect(
      replyToGitHubReviewThread(fixture.client, 7, thread.id, 'Reply', attempt()),
    ).rejects.toMatchObject({ code: 'resource_unavailable' })
    expect(fixture.calls.filter((call) => call.path.endsWith('/replies'))).toEqual([])
  })

  it('treats NOT_FOUND as unavailable without asserting deletion', async () => {
    const { client } = await githubFixture((_request, response) => {
      json(response, {
        data: { submitPullRequestReview: null },
        errors: [{ type: 'NOT_FOUND', message: 'private details' }],
      })
    })
    await expect(
      submitGitHubReview(client, 7, 'PRR_1', 'Summary', attempt()),
    ).rejects.toMatchObject({ code: 'review_unavailable', outcomeUnknown: false })
  })
})

function publishedThread() {
  return {
    ...thread,
    comments: {
      nodes: [
        {
          ...finding,
          state: 'SUBMITTED',
          pullRequestReview: { id: 'PRR_old', state: 'COMMENTED' },
        },
      ],
    },
  }
}
