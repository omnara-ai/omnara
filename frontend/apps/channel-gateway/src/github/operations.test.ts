import { describe, expect, it, vi } from 'vitest'

import { readGitHubOperation, sendGitHubOperation } from './operations'
import { input, operationFixture, options, requestID, scope } from './operations-test-support'
import { githubReviewMarker } from './reviews'
import {
  finding,
  json,
  mutationInputs,
  newCommit,
  noPrevious,
  oldCommit,
  pr,
  review,
  thread,
} from './test-support'

describe('GitHub operation ownership and publication', () => {
  it('returns the newest owned pending review with a stable ID tie-breaker, without choosing a send target', async () => {
    const latest = { ...review, createdAt: '2026-09-15T13:00:00Z' }
    for (const pending of [
      [review, { ...latest, id: 'PRR_z' }, { ...latest, id: 'PRR_a' }],
      [{ ...latest, id: 'PRR_a' }, review, { ...latest, id: 'PRR_z' }],
    ]) {
      const f = await operationFixture({ pending })
      await expect(
        sendGitHubOperation(f.client, f.core, input, scope, options()),
      ).rejects.toMatchObject({
        payload: { code: 'pending_review_exists', metadata: { review_id: 'PRR_a' } },
      })
      expect(mutationInputs(f.calls)).toEqual([])
    }
  })

  it('gives foreign pending conflict priority over owned drafts without exposing either reference', async () => {
    const f = await operationFixture({
      pending: [review, { ...review, id: 'PRR_other' }],
      ownershipForReview: (id) => (id === review.id ? 'owned' : 'other_agent'),
    })
    await expect(
      sendGitHubOperation(f.client, f.core, input, scope, options()),
    ).rejects.toHaveProperty('payload', { code: 'provider_pending_review_conflict' })
    expect(f.records).toEqual([])
    expect(mutationInputs(f.calls)).toEqual([])
  })
  it.each(['line', 'file'] as const)(
    'records the container before the first %s finding without rewriting args',
    async (subject) => {
      const f = await operationFixture()
      const send = {
        ...input,
        params:
          subject === 'file'
            ? {
                review_comment: true,
                commit_id: oldCommit,
                path: 'src/main.ts',
                subject_type: 'file',
              }
            : input.params,
      }
      const original = JSON.stringify(send)
      const result = await sendGitHubOperation(f.client, f.core, send, scope, options())
      expect(f.events).toEqual(['create', 'record', 'finding'])
      expect(JSON.stringify(send)).toBe(original)
      expect(f.records[0]).toEqual({
        scope: { request_id: requestID, agent_id: scope.agent_id, channel_id: scope.channel_id },
        observation: {
          review_id: review.id,
          commit_id: oldCommit,
          creating_tool_call_id: requestID,
        },
        evidence: 'create_response',
      })
      expect(result).toMatchObject({
        publication: 'draft',
        message_id: finding.id,
        message_channel: 'reply_channel',
        metadata: { review_id: review.id, commit_id: oldCommit },
        reply_channel: {
          implementation_key: 'github_review_thread',
          provider_ref: 'repo:456:pr:7:comment:PRRC_1',
          provider_metadata: { thread_id: thread.id },
        },
      })
      expect(mutationInputs(f.calls)[0]).toMatchObject({
        body: githubReviewMarker(requestID),
        commitOID: oldCommit,
      })
    },
  )

  it('stops after factual acknowledgment denies continuation, preserving the owned reference', async () => {
    const f = await operationFixture({ continue: false })
    await expect(
      sendGitHubOperation(f.client, f.core, input, scope, options()),
    ).rejects.toMatchObject({
      outcomeUnknown: false,
      payload: {
        code: 'review_finding_failed',
        metadata: { review_id: review.id, commit_id: oldCommit },
      },
    })
    expect(f.events).toEqual(['create', 'record'])
  })

  it('retains the known creator when recording fails and never dispatches a finding', async () => {
    const f = await operationFixture({ record: () => Response.json({}, { status: 409 }) })
    await expect(
      sendGitHubOperation(f.client, f.core, input, scope, options()),
    ).rejects.toMatchObject({
      outcomeUnknown: false,
      payload: { code: 'review_finding_failed', metadata: { review_id: review.id } },
    })
    expect(f.events).toEqual(['create', 'record'])
  })

  it('records a known create response after Stop using a fresh bounded callback, without another native mutation', async () => {
    const controller = new AbortController()
    let callbackSignal: AbortSignal | undefined
    const f = await operationFixture({
      record: (_request, signal) => {
        callbackSignal = signal
        return Response.json({ recorded: true, continue: false })
      },
    })
    const query = f.client.query.bind(f.client)
    vi.spyOn(f.client, 'query').mockImplementation(async (document, variables, schema, context) => {
      const result = await query(document, variables, schema, context)
      if (document === 'createReview') controller.abort()
      return result
    })
    await expect(
      sendGitHubOperation(f.client, f.core, input, scope, options(controller.signal)),
    ).rejects.toMatchObject({ code: 'canceled', outcomeUnknown: true })
    await vi.waitFor(() => {
      expect(f.records).toHaveLength(1)
    })
    expect(callbackSignal?.aborted).toBe(false)
    expect(f.events).toEqual(['create', 'record'])
    await vi.waitFor(
      () => {
        expect(callbackSignal?.aborted).toBe(true)
      },
      { timeout: 3000 },
    )
  })

  it('never repeats an unknown create after a lost acknowledgment', async () => {
    let creates = 0
    const f = await operationFixture({
      native: (request, response) => {
        if (!request.query.includes('GitHubCreateReview')) return false
        creates += 1
        response.destroy()
        return true
      },
    })
    await expect(
      sendGitHubOperation(f.client, f.core, input, scope, options()),
    ).rejects.toMatchObject({
      outcomeUnknown: true,
      attempts: 1,
      payload: { code: 'review_operation_unknown' },
    })
    expect(creates).toBe(1)
    expect(f.records).toEqual([])
  })

  it('retains the recorded identity on an unknown finding outcome and never repeats the finding', async () => {
    let findings = 0
    const f = await operationFixture({
      native: (request, response) => {
        if (!request.query.includes('GitHubAddFinding')) return false
        findings += 1
        response.destroy()
        return true
      },
    })
    await expect(
      sendGitHubOperation(f.client, f.core, input, scope, options()),
    ).rejects.toMatchObject({
      outcomeUnknown: true,
      attempts: 1,
      payload: {
        code: 'review_operation_unknown',
        metadata: { review_id: review.id, commit_id: oldCommit },
      },
    })
    expect(f.events).toEqual(['create', 'record'])
    expect(findings).toBe(1)
  })

  it('does not create from an incomplete native pending listing', async () => {
    const f = await operationFixture({
      native: (request, response) => {
        if (!request.query.includes('GitHubPendingReviews')) return false
        json(response, {
          data: {
            node: {
              id: 'R_selected',
              pullRequest: {
                ...pr,
                reviews: { nodes: [], pageInfo: { hasNextPage: true, endCursor: 'more' } },
              },
            },
          },
        })
        return true
      },
    })
    await expect(
      sendGitHubOperation(f.client, f.core, input, scope, options()),
    ).rejects.toMatchObject({ payload: { code: 'review_state_unavailable' } })
    expect(mutationInputs(f.calls)).toEqual([])
  })

  it('rejects a child address whose pinned root does not match the native thread', async () => {
    const f = await operationFixture()
    const changed = {
      ...f.threadInput,
      destination: {
        ...f.threadInput.destination,
        provider_ref: 'repo:456:pr:7:comment:PRRC_other',
      },
    }
    await expect(
      sendGitHubOperation(f.client, f.core, changed, scope, options()),
    ).rejects.toMatchObject({ payload: { code: 'invalid_address' } })
    expect(f.lookups).toEqual([])
    expect(mutationInputs(f.calls)).toEqual([])
  })

  it('recovers an exact marker with null native commit and returns the core pin without sending', async () => {
    const f = await operationFixture({
      native: (request, response) => {
        if (!request.query.includes('GitHubPendingReviews')) return false
        json(response, {
          data: {
            node: {
              id: 'R_selected',
              pullRequest: {
                ...pr,
                reviews: {
                  nodes: [{ ...review, commit: null, body: githubReviewMarker(requestID) }],
                  pageInfo: { hasNextPage: false, endCursor: null },
                },
              },
            },
          },
        })
        return true
      },
    })
    await expect(
      sendGitHubOperation(f.client, f.core, input, scope, options()),
    ).rejects.toMatchObject({
      payload: {
        code: 'pending_review_exists',
        metadata: { review_id: review.id, commit_id: oldCommit },
      },
    })
    expect(f.lookups[0]?.observations).toEqual([
      { review_id: review.id, creating_tool_call_id: requestID },
    ])
    expect(f.records[0]?.evidence).toBe('marker')
    expect(f.records[0]?.observation).not.toHaveProperty('commit_id')
    expect(mutationInputs(f.calls)).toEqual([])
  })

  it.each(['other_agent', 'unknown'] as const)(
    'blocks parallel foreign/unknown draft replies (%s) before any mutation',
    async (ownership) => {
      const f = await operationFixture({ ownership })
      const results = await Promise.allSettled(
        [scope, { ...scope, agent_id: 'agt_bbbbbbbbbbbbbbbbbbbbbbbbbb' }].map((actor) =>
          sendGitHubOperation(f.client, f.core, f.threadInput, actor, options()),
        ),
      )
      for (const result of results) {
        expect(result.status).toBe('rejected')
        if (result.status !== 'rejected') throw new Error('foreign draft was mutated')
        expect(result.reason).toMatchObject({ payload: { code: 'review_not_owned' } })
      }
      expect(f.lookups).toHaveLength(2)
      expect(mutationInputs(f.calls)).toEqual([])
    },
  )

  it('requires the core owner result before forwarding the exact pending review ID', async () => {
    const f = await operationFixture()
    await sendGitHubOperation(f.client, f.core, f.threadInput, scope, options())
    expect(f.lookups[0]?.observations).toEqual([{ review_id: review.id }])
    expect(mutationInputs(f.calls)).toEqual([
      {
        pullRequestReviewThreadId: thread.id,
        pullRequestReviewId: review.id,
        body: input.message.text,
      },
    ])
  })

  it.each(['other_agent', 'unknown'] as const)(
    'never adopts an observed %s bot draft or exposes its private references',
    async (ownership) => {
      const f = await operationFixture({ pending: [review], ownership })
      await expect(
        sendGitHubOperation(f.client, f.core, input, scope, options()),
      ).rejects.toHaveProperty('payload', { code: 'provider_pending_review_conflict' })
      expect(mutationInputs(f.calls)).toEqual([])
    },
  )

  it('publishes an exact owned old-commit draft even with a null observed commit', async () => {
    const f = await operationFixture({
      native: (request, response) => {
        if (!request.query.includes('GitHubReviewIdentity')) return false
        json(response, { data: { node: { ...review, commit: null, pullRequest: pr } } })
        return true
      },
    })
    await sendGitHubOperation(
      f.client,
      f.core,
      { ...input, params: { publish_review: true, review_id: review.id } },
      scope,
      options(),
    )
    expect(mutationInputs(f.calls)).toEqual([
      { pullRequestReviewId: review.id, event: 'COMMENT', body: input.message.text },
    ])
  })

  it('retains the creator pin in a successful finding whose native comment commit is null', async () => {
    const f = await operationFixture({
      native: (request, response) => {
        if (!request.query.includes('GitHubAddFinding')) return false
        json(response, {
          data: {
            addPullRequestReviewThread: {
              thread: { ...thread, comments: { nodes: [{ ...finding, originalCommit: null }] } },
            },
          },
        })
        return true
      },
    })
    const result = await sendGitHubOperation(f.client, f.core, input, scope, options())
    expect(result.metadata).toMatchObject({ review_id: review.id, commit_id: oldCommit })
  })

  it('rejects a new finding whose commit differs from the creator pin', async () => {
    const f = await operationFixture()
    await expect(
      sendGitHubOperation(
        f.client,
        f.core,
        { ...input, params: { ...input.params, review_id: review.id, commit_id: newCommit } },
        scope,
        options(),
      ),
    ).rejects.toMatchObject({
      payload: { code: 'review_commit_mismatch', metadata: { commit_id: oldCommit } },
    })
    expect(mutationInputs(f.calls)).toEqual([])
  })

  it('keeps timeline comments independent of pending reviews', async () => {
    const f = await operationFixture({ pending: [review], ownership: 'other_agent' })
    await sendGitHubOperation(f.client, f.core, { ...input, params: {} }, scope, options())
    expect(f.events).toEqual(['timeline'])
    expect(f.lookups).toEqual([])
  })

  it('returns a known finding and child when continuation grants are absent; core can report registration failure without resend', async () => {
    const f = await operationFixture()
    const result = await sendGitHubOperation(
      f.client,
      f.core,
      { ...input, reply_channel_grants: undefined },
      scope,
      options(),
    )
    expect(result).toMatchObject({
      publication: 'draft',
      message_channel: 'reply_channel',
      message_id: finding.id,
    })
    expect(result.reply_channel?.provider_ref).toContain(finding.id)
    expect(f.events).toEqual(['create', 'record', 'finding'])
  })

  it('shares three safe retries across creation and finding without recreating a successful container', async () => {
    let creates = 0,
      findings = 0
    const f = await operationFixture({
      native: (request, response) => {
        if (request.query.includes('GitHubCreateReview') && ++creates <= 2) {
          response.setHeader('retry-after', '0')
          json(response, {}, 429)
          return true
        }
        if (request.query.includes('GitHubAddFinding')) {
          findings += 1
          response.setHeader('retry-after', '0')
          json(response, {}, 429)
          return true
        }
        return false
      },
    })
    await expect(
      sendGitHubOperation(f.client, f.core, input, scope, options()),
    ).rejects.toMatchObject({
      code: 'retries_exhausted',
      attempts: 4,
      payload: { code: 'review_finding_failed', metadata: { review_id: review.id } },
    })
    expect(creates).toBe(3)
    expect(findings).toBe(2)
    expect(f.records).toHaveLength(1)
  })
})

describe('GitHub operation history privacy', () => {
  it.each(['other_agent', 'unknown'] as const)(
    'omits %s pending comments but preserves published history and pagination',
    async (ownership) => {
      const f = await operationFixture({
        ownership,
        native: (request, response) => {
          if (!request.query.includes('query GitHubReviewThread(')) return false
          json(response, {
            data: {
              node: {
                ...thread,
                comments: {
                  nodes: [
                    finding,
                    { ...finding, id: 'PRRC_public', body: 'Visible', state: 'SUBMITTED' },
                  ],
                  pageInfo: { hasPreviousPage: true, startCursor: 'older' },
                },
              },
            },
          })
          return true
        },
      })
      const result = await readGitHubOperation(
        f.client,
        f.core,
        { destination: f.threadInput.destination, limit: 2 },
        scope,
        options(),
      )
      expect(result.messages.map((message) => message.content.text)).toEqual(['Visible'])
      expect(result).toMatchObject({
        coverage: 'partial',
        coverage_reason: 'private_pending_reviews_omitted',
      })
      expect(result.next_cursor).toEqual(expect.any(String))
      expect(JSON.stringify(result)).not.toContain(finding.body)
      expect(f.lookups[0]?.observations).toEqual([{ review_id: review.id }])
      expect(mutationInputs(f.calls)).toEqual([])
    },
  )

  it('returns owned pending history only after lookup and never grants a channel while reading', async () => {
    const f = await operationFixture({
      native: (request, response) => {
        if (!request.query.includes('query GitHubReviewThread(')) return false
        json(response, {
          data: { node: { ...thread, comments: { nodes: [finding], pageInfo: noPrevious } } },
        })
        return true
      },
    })
    const result = await readGitHubOperation(
      f.client,
      f.core,
      { destination: f.threadInput.destination, limit: 1 },
      scope,
      options(),
    )
    expect(result.messages[0]).toMatchObject({
      publication: 'draft',
      content: { text: finding.body },
    })
    expect(f.lookups).toHaveLength(1)
    expect(f.records).toEqual([])
    expect(mutationInputs(f.calls)).toEqual([])
  })
})
