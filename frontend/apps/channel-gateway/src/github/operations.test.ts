import { describe, expect, it, vi } from 'vitest'

import { readGitHubOperation, sendGitHubOperation } from './operations'
import { input, operationFixture, options, scope } from './operations-test-support'
import { finding, json, mutationInputs, noPrevious, oldCommit, pr, thread } from './test-support'

async function fixture(settings?: Parameters<typeof operationFixture>[0]) {
  const f = await operationFixture(settings)
  return {
    ...f,
    send: (send = input, retry = options(), owner = scope) =>
      sendGitHubOperation(f.client, send, owner, retry, f.publishDefinition),
  }
}

describe('GitHub immediate operation orchestration', () => {
  it('publishes the generic thread definition before first native send and returns the actual child', async () => {
    const f = await fixture()
    const result = await f.send()
    expect(f.events).toEqual(['definition', 'comment'])
    expect(f.publishDefinition).toHaveBeenCalledWith(
      scope,
      expect.objectContaining({
        implementation_key: 'github_review_thread',
        kind: 'GITHUB_REVIEW_THREAD',
      }),
      expect.any(AbortSignal),
    )
    expect(result).toMatchObject({
      publication: 'published',
      message_channel: 'reply_channel',
      message_id: finding.id,
      metadata: { commit_id: oldCommit, path: finding.path, line: finding.line },
      reply_channel: {
        implementation_key: 'github_review_thread',
        provider_ref_kind: 'review_thread',
        provider_ref: `repo:456:pr:7:comment:${finding.id}`,
        provider_metadata: { thread_id: thread.id },
      },
    })
    expect(result.continuation_error).toBeUndefined()
    expect(
      f.calls.filter((call) => JSON.stringify(call.body ?? null).includes('GitHubCommentIdentity')),
    ).toHaveLength(1)
    expect(
      f.calls.some((call) => JSON.stringify(call.body ?? null).includes('GitHubInboundComment')),
    ).toBe(false)
    expect(mutationInputs(f.calls)).toEqual([])
  })

  it('publishes at destination with no child definition/discovery when no reply grants were accepted', async () => {
    const f = await fixture()
    const result = await f.send({ ...input, reply_channel_grants: undefined })
    expect(result).toMatchObject({
      publication: 'published',
      message_channel: 'destination',
      message_id: finding.id,
    })
    expect(result.reply_channel).toBeUndefined()
    expect(result.continuation_error).toBeUndefined()
    expect(f.publishDefinition).not.toHaveBeenCalled()
    expect(f.events).toEqual(['comment'])
    expect(
      f.calls.some((call) => JSON.stringify(call.body ?? null).includes('GitHubInbound')),
    ).toBe(false)
  })

  it('does not send when the generic child definition cannot be established', async () => {
    const f = await fixture()
    f.publishDefinition.mockRejectedValue(new Error('private core failure'))
    await expect(f.send()).rejects.toMatchObject({
      code: 'permanent_failure',
      outcomeUnknown: false,
    })
    expect(f.calls).toEqual([])
  })

  it('publishes a reply in its existing thread without creating a child', async () => {
    const f = await fixture()
    const result = await f.send(f.threadInput)
    expect(result).toMatchObject({
      publication: 'published',
      message_channel: 'destination',
      message_id: 'PRRC_reply',
    })
    expect(result.reply_channel).toBeUndefined()
    expect(f.publishDefinition).not.toHaveBeenCalled()
    expect(f.events).toEqual(['reply'])
  })

  it.each(['inline', 'reply'] as const)(
    'does not mutate any observed bot draft for %s',
    async (action) => {
      const f = await fixture({ pending: true })
      await expect(f.send(action === 'reply' ? f.threadInput : input)).rejects.toMatchObject({
        code: 'permanent_failure',
        outcomeUnknown: false,
      })
      expect(f.calls.filter((call) => call.path.includes('/pulls/7/comments'))).toEqual([])
      expect(mutationInputs(f.calls)).toEqual([])
    },
  )

  it.each(['lost-ack', 'server-error', 'malformed', 'wrong-status'] as const)(
    'never retries an uncertain REST %s mutation',
    async (problem) => {
      const f = await fixture({
        rest: (_call, response) => {
          if (problem === 'lost-ack') response.destroy()
          else if (problem === 'server-error') json(response, {}, 503)
          else if (problem === 'wrong-status') json(response, { node_id: finding.id }, 200)
          else json(response, { private: 'invalid native acknowledgment' }, 201)
          return true
        },
      })
      await expect(f.send()).rejects.toMatchObject({ outcomeUnknown: true, attempts: 1 })
      expect(f.events).toEqual(['definition', 'comment'])
    },
  )

  it.each(['unavailable', 'pending-comment', 'pending-parent', 'wrong-id', 'wrong-pr'] as const)(
    'does not resend an accepted inline comment after %s readback',
    async (problem) => {
      const f = await fixture({
        native: (request, response) => {
          if (!request.query.includes('GitHubCommentIdentity')) return false
          if (problem === 'unavailable') json(response, {}, 503)
          else
            json(response, {
              data: {
                node: {
                  ...finding,
                  id: problem === 'wrong-id' ? 'PRRC_other' : finding.id,
                  state: problem === 'pending-comment' ? 'PENDING' : 'SUBMITTED',
                  pullRequestReview: {
                    id: 'PRR_any',
                    state: problem === 'pending-parent' ? 'PENDING' : 'COMMENTED',
                  },
                  pullRequest: problem === 'wrong-pr' ? { ...pr, number: 8 } : pr,
                },
              },
            })
          return true
        },
      })
      await expect(f.send()).rejects.toMatchObject({ outcomeUnknown: true, attempts: 1 })
      expect(f.events).toEqual(['definition', 'comment'])
    },
  )

  it.each(['unavailable', 'missing', 'foreign'] as const)(
    'preserves known publication when bounded child discovery is %s',
    async (problem) => {
      const f = await fixture({
        native: (request, response) => {
          if (!request.query.includes('GitHubInboundThreads')) return false
          if (problem === 'unavailable') json(response, {}, 503)
          else
            json(response, {
              data: {
                node: {
                  id: 'R_selected',
                  pullRequest: {
                    ...pr,
                    number: problem === 'foreign' ? 8 : 7,
                    reviewThreads: { nodes: [], pageInfo: { hasNextPage: false, endCursor: null } },
                  },
                },
              },
            })
          return true
        },
      })
      const result = await f.send()
      expect(result).toMatchObject({
        publication: 'published',
        message_id: finding.id,
        message_channel: 'destination',
        continuation_error: { code: 'reply_channel_unavailable' },
      })
      expect(result.reply_channel).toBeUndefined()
      expect(f.events).toEqual(['definition', 'comment'])
      expect(
        f.calls.filter((call) =>
          JSON.stringify(call.body ?? null).includes('GitHubInboundThreads'),
        ),
      ).toHaveLength(1)
    },
  )

  it('advances bounded native thread pages without repeating creation', async () => {
    const f = await fixture({
      native: (request, response) => {
        if (!request.query.includes('GitHubInboundThreads')) return false
        const second = request.variables.after === 'page-2'
        json(response, {
          data: {
            node: {
              id: 'R_selected',
              pullRequest: {
                ...pr,
                reviewThreads: {
                  nodes: second
                    ? [thread]
                    : [
                        {
                          ...thread,
                          id: 'PRRT_other',
                          comments: { nodes: [{ id: 'PRRC_other', state: 'SUBMITTED' }] },
                        },
                      ],
                  pageInfo: { hasNextPage: !second, endCursor: second ? null : 'page-2' },
                },
              },
            },
          },
        })
        return true
      },
    })
    expect(await f.send()).toMatchObject({
      reply_channel: { provider_metadata: { thread_id: thread.id } },
    })
    expect(f.events).toEqual(['definition', 'comment'])
  })

  it('retries a definite REST rate rejection under the same aggregate budget', async () => {
    let attempts = 0
    const f = await fixture({
      rest: (_call, response) => {
        attempts += 1
        if (attempts !== 1) return false
        response.setHeader('retry-after', '0')
        json(response, {}, 429)
        return true
      },
    })
    expect(await f.send()).toMatchObject({ publication: 'published' })
    expect(attempts).toBe(2)
  })

  it('caps safe REST rejection retries at four attempts', async () => {
    const f = await fixture({
      rest: (_call, response) => {
        response.setHeader('retry-after', '0')
        json(response, {}, 429)
        return true
      },
    })
    await expect(f.send()).rejects.toMatchObject({
      code: 'retries_exhausted',
      outcomeUnknown: false,
      attempts: 4,
    })
    expect(f.events.filter((event) => event === 'comment')).toHaveLength(4)
  })

  it.each(['canceled', 'expired'] as const)(
    'does no work before a %s operation',
    async (problem) => {
      const f = await fixture()
      const controller = new AbortController()
      if (problem === 'canceled') controller.abort()
      const retry = options(controller.signal)
      if (problem === 'expired') retry.deadlineMs = Date.now() - 1
      await expect(f.send(input, retry)).rejects.toMatchObject({
        outcomeUnknown: false,
        attempts: 0,
      })
      expect(f.events).toEqual([])
      expect(f.calls).toEqual([])
    },
  )

  it.each(['mutation', 'readback', 'discovery'] as const)(
    'never repeats an explicit write when cancellation arrives during %s',
    async (stage) => {
      const controller = new AbortController()
      const f = await fixture({
        rest: (_call, response) => {
          if (stage !== 'mutation') return false
          controller.abort()
          response.destroy()
          return true
        },
        native: (request, response) => {
          const document = stage === 'readback' ? 'GitHubCommentIdentity' : 'GitHubInboundThreads'
          if (stage === 'mutation' || !request.query.includes(document)) return false
          controller.abort()
          response.destroy()
          return true
        },
      })
      await expect(f.send(input, options(controller.signal))).rejects.toMatchObject({
        outcomeUnknown: true,
        attempts: 1,
      })
      expect(f.events).toEqual(['definition', 'comment'])
    },
  )

  it('bounds an actual in-flight mutation by the existing deadline', async () => {
    let closed = false
    const f = await fixture({
      rest: (_call, response) => {
        response.on('close', () => {
          closed = true
        })
        return true
      },
    })
    await expect(
      f.send(input, { ...options(), deadlineMs: Date.now() + 2_000 }),
    ).rejects.toMatchObject({ outcomeUnknown: true, attempts: 1 })
    await vi.waitFor(() => {
      expect(closed).toBe(true)
    })
    expect(f.events).toEqual(['definition', 'comment'])
  })

  it.each(['project', 'install', 'agent', 'channel'] as const)(
    'requires the admitted %s scope',
    async (field) => {
      const f = await fixture()
      const owner = {
        ...scope,
        [`${field === 'install' ? 'integration_install' : field}_id`]: 'invalid',
      }
      await expect(f.send(input, options(), owner)).rejects.toMatchObject({ outcomeUnknown: false })
      expect(f.calls).toEqual([])
      expect(f.publishDefinition).not.toHaveBeenCalled()
    },
  )

  it('reads only published history with no ownership callbacks or native mutations', async () => {
    const f = await fixture({
      native: (request, response) => {
        if (!request.query.includes('GitHubReviewThread(')) return false
        json(response, {
          data: {
            node: {
              ...thread,
              comments: {
                nodes: [
                  { ...finding, id: 'PRRC_private', body: 'Private draft', state: 'PENDING' },
                  finding,
                ],
                pageInfo: noPrevious,
              },
            },
          },
        })
        return true
      },
    })
    const result = await readGitHubOperation(
      f.client,
      { destination: f.threadInput.destination, limit: 2 },
      scope,
      options(),
    )
    expect(result).toMatchObject({ coverage: 'partial', messages: [{ message_id: finding.id }] })
    expect(JSON.stringify(result)).not.toContain('Private draft')
    expect(f.events).toEqual([])
    expect(mutationInputs(f.calls)).toEqual([])
  })
})
