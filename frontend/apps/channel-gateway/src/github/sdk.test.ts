import { describe, expect, it, vi } from 'vitest'

import { retryOperation } from '../operations/retry'
import { postGitHubTimelineComment, replyToGitHubReviewThread } from './messages'
import { operationFixture } from './operations-test-support'
import { attempt, comment, finding, githubFixture, json, restComment, thread } from './test-support'

describe('GitHub adapter public Octokit boundary', () => {
  it('preserves exact timeline Markdown, emoji placeholders and narrowed credentials', async () => {
    const text = '**Original** `x` @example\n```ts\nconst s = ":rocket: {{emoji:rocket}}"\n```\n'
    const f = await githubFixture(
      () => {
        throw new Error('unexpected GraphQL')
      },
      (_call, response) => {
        json(response, { ...restComment, body: text }, 201)
      },
    )
    expect(await postGitHubTimelineComment(f.client, 7, text, attempt())).toEqual({
      id: comment.id,
      text,
      createdAt: comment.createdAt,
      authorRef: comment.author.login,
      publication: 'published',
    })
    expect(f.calls.at(-1)).toMatchObject({
      path: '/repos/new-owner/renamed/issues/7/comments',
      body: { body: text },
      authorization: 'Bearer local-installation-token',
    })
    expect(f.calls[0]?.body).toEqual({
      repository_ids: [456],
      permissions: { pull_requests: 'write', issues: 'read', metadata: 'read' },
    })
    expect(f.calls.filter((call) => call.path.includes('/comments'))).toHaveLength(1)
  })

  it('preserves a large decimal reply root and raw body, then verifies publication', async () => {
    const root = '9223372036854775807'
    const text = 'Reply with `{{emoji:rocket}}`'
    const f = await operationFixture({
      native: (request, response) => {
        if (!request.query.includes('GitHubThreadIdentity')) return false
        json(response, {
          data: {
            node: { ...thread, comments: { nodes: [{ ...finding, fullDatabaseId: root }] } },
          },
        })
        return true
      },
      rest: (_call, response) => {
        response.writeHead(201, { 'content-type': 'application/json' })
        // Literal JSON exercises the installed Octokit decoder with a large ID.
        response.end('{"id":9007199254740993,"node_id":"PRRC_reply"}')
        return true
      },
    })
    expect(
      await replyToGitHubReviewThread(f.client, 7, thread.id, finding.id, text, attempt()),
    ).toMatchObject({ id: 'PRRC_reply', publication: 'published', replyTo: finding.id })
    expect(f.calls.filter((call) => call.path.endsWith('/replies'))).toEqual([
      expect.objectContaining({
        path: `/repos/new-owner/renamed/pulls/7/comments/${root}/replies`,
        body: { body: text },
      }),
    ])
    expect(f.calls.at(-1)?.body).toMatchObject({ variables: { comment: 'PRRC_reply' } })
  })

  it.each(['large IDs', 'deleted author'] as const)(
    'keeps native timeline facts with %s without numeric coercion',
    async (scenario) => {
      const f = await githubFixture(
        () => {
          throw new Error('unexpected GraphQL')
        },
        (_call, response) => {
          response.writeHead(201, { 'content-type': 'application/json' })
          response.end(
            scenario === 'large IDs'
              ? JSON.stringify(restComment)
                  .replace('"id":101', '"id":9007199254740993')
                  .replace('"id":102', '"id":9007199254740995')
              : JSON.stringify({ ...restComment, user: null }),
          )
        },
      )
      expect(await postGitHubTimelineComment(f.client, 7, comment.body, attempt())).toMatchObject({
        id: comment.id,
        text: comment.body,
        authorRef: scenario === 'deleted author' ? undefined : comment.author.login,
      })
    },
  )

  it('keeps a malformed acknowledgment unknown without another POST', async () => {
    const f = await githubFixture(
      () => {
        throw new Error('unexpected GraphQL')
      },
      (_call, response) => {
        json(response, { ...restComment, node_id: null }, 201)
      },
    )
    await expect(
      retryOperation(attempt(), (context) =>
        postGitHubTimelineComment(f.client, 7, 'hello', context),
      ),
    ).rejects.toMatchObject({ outcomeUnknown: true, attempts: 1 })
    expect(f.calls.filter((call) => call.path.endsWith('/comments'))).toHaveLength(1)
  })

  it('isolates concurrent SDK request signals and bounds direct-call response reads', async () => {
    let stalled = false
    let closed = false
    const f = await githubFixture(
      () => {
        throw new Error('unexpected GraphQL')
      },
      (call, response) => {
        if (JSON.stringify(call.body).includes('stalled')) {
          stalled = true
          response.on('close', () => {
            closed = true
          })
          response.writeHead(201, { 'content-type': 'application/json' })
          response.write('{"id":')
        } else json(response, restComment, 201)
      },
    )
    const failed = f.client
      .publishedTimeline(7, 'stalled', attempt(undefined, 500))
      .catch((cause: unknown) => cause)
    await vi.waitFor(() => {
      expect(stalled).toBe(true)
    })
    const successful = await f.client.publishedTimeline(7, comment.body, attempt())
    expect(successful.id).toBe(comment.id)
    expect(await failed).toMatchObject({ outcomeUnknown: true, retryable: false })
    await vi.waitFor(() => {
      expect(closed).toBe(true)
    })
    expect(f.calls.filter((call) => call.path.endsWith('/comments'))).toHaveLength(2)
  })
})
