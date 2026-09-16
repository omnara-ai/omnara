import { randomUUID } from 'node:crypto'

import { type ChannelConnectorEventReceipt, schemas } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'
import { z } from 'zod'

import { CoreClient } from '../core-client'
import { ReceiptConsumer } from '../receipt-consumer'
import { createGitHubFactory } from './factory'
import {
  contexts,
  coreFixture,
  event,
  nativeComment,
  receipt,
  webhook,
} from './inbound-test-support'
import { json, localServer, requestBody } from './test-support'

describe('GitHub cached authentication receipt recovery', () => {
  it('keeps the same rejected-token receipt pending, refreshes auth and delivers exactly once', async () => {
    let tokens = 0
    let viewers = 0
    let rejections = 0
    let rejectOldToken = false
    const native = await localServer(async (request, response) => {
      if (request.url === '/app/installations/123/access_tokens') {
        expect(await requestBody(request)).toEqual({
          repository_ids: [456],
          permissions: { pull_requests: 'write', issues: 'read', metadata: 'read' },
        })
        tokens += 1
        json(response, {
          token: `local-${tokens}`,
          expires_at: new Date(Date.now() + 3_600_000).toISOString(),
        })
      } else if (request.url === '/repositories/456') {
        if (rejectOldToken && request.headers.authorization === 'Bearer local-1') {
          rejections += 1
          json(response, { message: 'PRIVATE rejected token' }, 401)
        } else
          json(response, {
            id: 456,
            node_id: 'R_selected',
            name: 'project',
            owner: { login: 'example' },
          })
      } else if (request.url === '/graphql') {
        expect(await requestBody(request)).toEqual({
          query: 'query GitHubViewer { viewer { id login } }',
          variables: {},
        })
        viewers += 1
        json(response, { data: { viewer: { id: 'U_bot', login: 'example[bot]' } } })
      } else throw new Error('unexpected provider request')
    })
    const context = contexts()
    const core = coreFixture()
    const runtime = await createGitHubFactory({ core, apiUrl: native }).create(context.factory)
    const behavior = runtime.processReceipt
    if (!behavior) throw new Error('missing receipt behavior')
    const payload = {
      ...webhook,
      action: 'created',
      issue: { ...webhook.pull_request, pull_request: {} },
      comment: nativeComment,
    }
    // An own-bot receipt warms the real factory memo without creating input.
    const warm = {
      ...receipt(
        event(
          {
            ...payload,
            comment: {
              ...nativeComment,
              id: 34,
              user: { ...nativeComment.user, node_id: 'U_bot' },
            },
          },
          'issue_comment',
        ),
      ),
      receipt_id: 'irec_bbbbbbbbbbbbbbbbbbbbbbbbbb',
    }
    const saved = receipt(event(payload, 'issue_comment'))
    const queue: ChannelConnectorEventReceipt[] = [warm, saved]
    const claimed: ChannelConnectorEventReceipt[] = []
    const completions: { receipt_id: string; state: string; last_error?: { code: string } }[] = []
    const endpoint = await localServer(async (request, response) => {
      expect(request.headers.authorization).toBe('Bearer local-core-token')
      const body = await requestBody(request)
      if (request.url === '/channel-connector/events/claim-next') {
        schemas.zClaimNextChannelConnectorEventRequest.parse(body)
        const next = queue.shift()
        if (!next) {
          response.writeHead(204)
          response.end()
          return
        }
        claimed.push(next)
        json(response, z.json().parse(JSON.parse(JSON.stringify(next))))
      } else {
        const current = claimed.at(-1)
        if (!current) throw new Error('completion without claim')
        expect(request.url).toBe(
          `/channel-connector/apps/${current.integration_app_id}/installations/${current.integration_install_id}/events/${current.receipt_id}/complete`,
        )
        expect(schemas.zCompleteChannelConnectorEventRequest.safeParse(body).success).toBe(true)
        const completion = z
          .object({
            state: z.string(),
            lease_token: z.string(),
            lease_generation: z.number(),
            last_error: z.object({ code: z.string() }).optional(),
          })
          .parse(body)
        expect(completion.lease_token).toBe(current.lease_token)
        expect(completion.lease_generation).toBe(current.lease_generation)
        completions.push({ ...completion, receipt_id: current.receipt_id })
        if (current.receipt_id === warm.receipt_id) rejectOldToken = true
        json(response, { receipt_id: current.receipt_id, state: completion.state })
      }
    })
    const client = new CoreClient({ baseUrl: endpoint, token: 'local-core-token' })
    const consumer = new ReceiptConsumer({
      capabilities: [{ connector_key: 'omnara', provider: 'github' }],
      client,
      behavior,
      workBudget: context.budget,
      logger: context.factory.logger,
      maxConcurrentEvents: 1,
      maxAttempts: 3,
      leaseMs: 30_000,
      claimTimeoutMs: 2_000,
      behaviorTimeoutMs: 10_000,
      completionTimeoutMs: 2_000,
      idlePollMs: 10,
    })
    // Don't stop between claims: an old aborted HTTP handler can still consume
    // the next queued lease, which this fake doesn't reclaim after expiry.
    const stop = new AbortController()
    const running = consumer.run(stop.signal)
    try {
      await vi.waitFor(
        () => {
          expect(completions).toHaveLength(2)
        },
        { timeout: 5_000 },
      )
      expect(completions.map((completion) => completion.state)).toEqual(['completed', 'pending'])
      expect(completions[1]).toMatchObject({
        receipt_id: saved.receipt_id,
        last_error: { code: 'retryable_failure' },
      })
      expect(core.deliverWorkflow).not.toHaveBeenCalled()
      expect(tokens).toBe(1)
      expect(viewers).toBe(1)
      expect(rejections).toBe(1)
      // Fake Core makes this same pending receipt eligible with a fresh lease;
      // the consumer/factory/native client must recover without another event.
      queue.push({ ...saved, lease_token: randomUUID(), lease_generation: 2, attempt_count: 2 })
      await vi.waitFor(
        () => {
          expect(completions).toHaveLength(3)
        },
        { timeout: 5_000 },
      )
      expect(completions[2]).toMatchObject({ receipt_id: saved.receipt_id, state: 'completed' })
      expect(claimed[2]?.payload).toEqual(saved.payload)
      expect(claimed[2]?.event_id).toBe(saved.event_id)
      expect(core.deliverWorkflow).toHaveBeenCalledOnce()
      expect(core.deliverWorkflow.mock.calls[0]?.[0].receipt_id).toBe(saved.receipt_id)
      expect(core.deliverWorkflow.mock.calls[0]?.[0].event_id).toBe(saved.event_id)
      expect(JSON.stringify(core.deliverWorkflow.mock.calls[0]?.[1].content_blocks)).toContain(
        nativeComment.body,
      )
      expect(tokens).toBe(2)
      expect(viewers).toBe(2)
      expect(rejections).toBe(1)
      expect(JSON.stringify(completions)).not.toContain('PRIVATE')
    } finally {
      stop.abort()
      await running
      await runtime.close()
    }
    await vi.waitFor(() => {
      expect(context.budget.usedBytes).toBe(0)
    })
  }, 15_000)
})
