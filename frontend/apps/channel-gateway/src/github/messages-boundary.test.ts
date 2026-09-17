import { describe, expect, it, vi } from 'vitest'

import { retryOperation } from '../operations/retry'
import { postGitHubTimelineComment, replyToGitHubReviewThread } from './messages'
import { operationFixture } from './operations-test-support'
import {
  attempt,
  comment,
  finding,
  githubFixture,
  json,
  mutationInputs,
  thread,
} from './test-support'

describe('GitHub message transport', () => {
  it('preserves exact timeline Markdown, emoji placeholders and narrowed credentials', async () => {
    const text = '**Original** `x` @example\n```ts\nconst s = ":rocket: {{emoji:rocket}}"\n```\n'
    const f = await githubFixture((_request, response) => {
      json(response, {
        data: { addComment: { commentEdge: { node: { ...comment, body: text } } } },
      })
    })
    expect(await postGitHubTimelineComment(f.client, 7, text, attempt())).toEqual({
      id: comment.id,
      text,
      createdAt: comment.createdAt,
      authorRef: comment.author.login,
      publication: 'published',
    })
    expect(mutationInputs(f.calls)).toEqual([{ subjectId: 'PR_selected', body: text }])
    expect(f.calls.at(-1)?.authorization).toBe('Bearer local-installation-token')
    expect(f.calls[0]?.body).toEqual({
      repository_ids: [456],
      permissions: { pull_requests: 'write', issues: 'read', metadata: 'read' },
    })
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
        // Consume the opaque node ID without rounding the numeric REST ID.
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

  it('preserves the opaque timeline ID when the author was deleted', async () => {
    const id = 'IC_9223372036854775807'
    const f = await githubFixture((_request, response) => {
      json(response, {
        data: { addComment: { commentEdge: { node: { ...comment, id, author: null } } } },
      })
    })
    expect(await postGitHubTimelineComment(f.client, 7, comment.body, attempt())).toMatchObject({
      id,
      text: comment.body,
      authorRef: undefined,
    })
  })

  it('keeps a malformed acknowledgment unknown without another mutation', async () => {
    const f = await githubFixture((_request, response) => {
      json(response, { data: { addComment: { commentEdge: { node: { ...comment, id: null } } } } })
    })
    await expect(
      retryOperation(attempt(), (context) =>
        postGitHubTimelineComment(f.client, 7, 'hello', context),
      ),
    ).rejects.toMatchObject({ outcomeUnknown: true, attempts: 1 })
    expect(mutationInputs(f.calls)).toHaveLength(1)
  })

  it('caps response bytes even when only unused fields are oversized', async () => {
    const f = await githubFixture((_request, response) => {
      json(response, {
        data: { addComment: { commentEdge: { node: comment } } },
        unused: 'x'.repeat(1024 * 1024),
      })
    })
    await expect(postGitHubTimelineComment(f.client, 7, 'hello', attempt())).rejects.toMatchObject({
      code: 'response_too_large',
      outcomeUnknown: true,
      retryable: false,
    })
    expect(mutationInputs(f.calls)).toHaveLength(1)
  })

  it('isolates concurrent request signals and bounds a stalled response read', async () => {
    let stalled = false
    let closed = false
    const f = await githubFixture((request, response) => {
      if (JSON.stringify(request.variables).includes('stalled')) {
        stalled = true
        response.on('close', () => {
          closed = true
        })
        response.writeHead(200, { 'content-type': 'application/json' })
        response.write('{"data":')
      } else json(response, { data: { addComment: { commentEdge: { node: comment } } } })
    })
    const failed = postGitHubTimelineComment(f.client, 7, 'stalled', attempt(undefined, 500)).catch(
      (cause: unknown) => cause,
    )
    await vi.waitFor(() => {
      expect(stalled).toBe(true)
    })
    const successful = await postGitHubTimelineComment(f.client, 7, comment.body, attempt())
    expect(successful.id).toBe(comment.id)
    expect(await failed).toMatchObject({ outcomeUnknown: true, retryable: false })
    await vi.waitFor(() => {
      expect(closed).toBe(true)
    })
    expect(mutationInputs(f.calls)).toHaveLength(2)
  })
})
