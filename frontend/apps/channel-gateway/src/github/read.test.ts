import { describe, expect, it } from 'vitest'

import { readGitHubHistory } from './read'
import {
  attempt,
  comment,
  finding,
  githubFixture,
  json,
  noPrevious,
  pr,
  review,
  thread,
} from './test-support'

describe('GitHub bounded history', () => {
  it('pages PR communication with an exact native cursor and explicit partial coverage', async () => {
    const beforeValues: unknown[] = []
    const fixture = await githubFixture((request, response) => {
      beforeValues.push(request.variables.before)
      const older = request.variables.before === 'older-page'
      json(response, {
        data: {
          node: {
            id: 'R_selected',
            pullRequest: {
              ...pr,
              timelineItems: {
                nodes: [
                  { ...comment, id: older ? 'IC_older' : 'IC_newer', __typename: 'IssueComment' },
                ],
                pageInfo: { hasPreviousPage: !older, startCursor: older ? 'end' : 'older-page' },
              },
            },
          },
        },
      })
    })
    const first = await readGitHubHistory(fixture.client, { number: 7, limit: 1 }, attempt())
    const second = await readGitHubHistory(
      fixture.client,
      { number: 7, limit: 1, cursor: first.nextCursor },
      attempt(),
    )
    expect(first).toMatchObject({
      coverage: 'partial',
      reason: 'timeline_excludes_inline_discussion_and_non_comment_events',
      messages: [{ id: 'IC_newer', text: comment.body }],
    })
    expect(second.messages.map((message) => message.id)).toEqual(['IC_older'])
    expect(second.nextCursor).toBeUndefined()
    expect(beforeValues).toEqual([undefined, 'older-page'])
    const count = fixture.calls.length
    await expect(
      readGitHubHistory(
        fixture.client,
        { number: 8, limit: 1, cursor: first.nextCursor },
        attempt(),
      ),
    ).rejects.toMatchObject({ code: 'invalid_history_cursor' })
    expect(fixture.calls).toHaveLength(count)
  })

  it('reads published thread messages with native reply identity', async () => {
    const fixture = await githubFixture((_request, response) => {
      json(response, {
        data: {
          node: {
            ...thread,
            comments: {
              nodes: [{ ...finding, replyTo: { id: 'PRRC_root' } }],
              pageInfo: noPrevious,
            },
          },
        },
      })
    })
    const page = await readGitHubHistory(
      fixture.client,
      { number: 7, threadID: 'PRRT_1', limit: 10 },
      attempt(),
    )
    expect(page).toMatchObject({
      coverage: 'complete',
      messages: [{ id: 'PRRC_1', replyTo: 'PRRC_root', publication: 'published' }],
    })
    expect(page.nextCursor).toBeUndefined()
  })

  it.each(['foreign', 'nonadvancing', 'oversized'])(
    'rejects %s thread responses without silently dropping content',
    async (problem) => {
      const fixture = await githubFixture((_request, response) => {
        json(response, {
          data: {
            node: {
              ...thread,
              pullRequest: problem === 'foreign' ? { ...pr, repository: { id: 'R_other' } } : pr,
              comments: {
                nodes:
                  problem === 'nonadvancing'
                    ? []
                    : [
                        {
                          ...finding,
                          body: problem === 'oversized' ? 'a'.repeat(64 * 1024 + 1) : finding.body,
                        },
                      ],
                pageInfo: { hasPreviousPage: problem === 'nonadvancing', startCursor: 'cursor' },
              },
            },
          },
        })
      })
      await expect(
        readGitHubHistory(fixture.client, { number: 7, threadID: 'PRRT_1', limit: 10 }, attempt()),
      ).rejects.toMatchObject({ outcomeUnknown: false })
    },
  )

  it('reports empty native bodies as omitted rather than pretending the page is complete', async () => {
    const fixture = await githubFixture((_request, response) => {
      json(response, {
        data: {
          node: {
            ...thread,
            comments: { nodes: [{ ...finding, body: '' }], pageInfo: noPrevious },
          },
        },
      })
    })
    const page = await readGitHubHistory(
      fixture.client,
      { number: 7, threadID: 'PRRT_1', limit: 1 },
      attempt(),
    )
    expect(page).toMatchObject({
      coverage: 'partial',
      reason: 'empty_comment_bodies_omitted',
      messages: [],
    })
  })

  it.each(['summary', 'comment', 'parent'] as const)(
    'omits pending %s content while advancing an empty filtered page',
    async (kind) => {
      const before: unknown[] = []
      const fixture = await githubFixture((request, response) => {
        before.push(request.variables.before)
        const older = request.variables.before === 'older'
        const pending =
          kind === 'summary'
            ? { ...review, body: 'Private summary', __typename: 'PullRequestReview' }
            : {
                ...finding,
                body: 'Private finding',
                state: kind === 'comment' ? 'PENDING' : 'SUBMITTED',
                pullRequestReview: { id: 'PRR_private', state: 'PENDING' },
              }
        const published = kind === 'summary' ? { ...comment, __typename: 'IssueComment' } : finding
        const page = {
          nodes: [older ? published : pending],
          pageInfo: { hasPreviousPage: !older, startCursor: older ? 'end' : 'older' },
        }
        json(response, {
          data: {
            node:
              kind === 'summary'
                ? { id: 'R_selected', pullRequest: { ...pr, timelineItems: page } }
                : { ...thread, comments: page },
          },
        })
      })
      const input = { number: 7, limit: 1, threadID: kind === 'summary' ? undefined : thread.id }
      const first = await readGitHubHistory(fixture.client, input, attempt())
      expect(first).toMatchObject({
        messages: [],
        coverage: 'partial',
        reason: 'private_pending_reviews_omitted',
      })
      expect(first.nextCursor).toBeDefined()
      const second = await readGitHubHistory(
        fixture.client,
        { ...input, cursor: first.nextCursor },
        attempt(),
      )
      expect(second.messages).toHaveLength(1)
      expect(second.messages[0]?.publication).toBe('published')
      expect(second.nextCursor).toBeUndefined()
      expect(before).toEqual([undefined, 'older'])
      expect(JSON.stringify([first, second])).not.toContain('Private')
    },
  )
})
