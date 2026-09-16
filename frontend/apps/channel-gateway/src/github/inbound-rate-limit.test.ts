import { randomUUID } from 'node:crypto'

import { schemas } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'
import { z } from 'zod'

import { CoreClient } from '../core-client'
import { ReceiptConsumer } from '../receipt-consumer'
import { processGitHubEvent } from './behavior'
import {
  contexts,
  coreFixture,
  event,
  nativeComment,
  receipt,
  webhook,
} from './inbound-test-support'
import { githubFixture, json, localServer, mutationInputs, requestBody } from './test-support'

/** Actual native transport, receipt consumer and generated completion HTTP;
 * fake Core controls retry eligibility rather than waiting for provider time.
 */
describe('GitHub inbound read throttling', () => {
  it.each([
    {
      name: 'typed primary',
      status: 200,
      remaining: '0',
      errors: [{ type: 'RATE_LIMITED' }],
      waitForReset: true,
    },
    {
      name: 'untyped exhausted quota',
      status: 200,
      remaining: '0',
      errors: [{ message: 'API rate limit exceeded' }],
      waitForReset: true,
    },
    {
      name: 'typed with remaining omitted',
      status: 200,
      remaining: undefined,
      errors: [{ type: 'RATE_LIMITED' }],
      waitForReset: true,
    },
    {
      name: 'untyped secondary HTTP200',
      status: 200,
      remaining: '4999',
      errors: [{ message: 'You have exceeded a secondary rate limit.' }],
      waitForReset: false,
    },
    {
      name: 'secondary message takes precedence over primary type',
      status: 200,
      remaining: '4999',
      errors: [{ type: 'RATE_LIMITED', message: 'You have exceeded a secondary rate limit.' }],
      waitForReset: false,
    },
    { name: 'secondary HTTP403', status: 403, remaining: '4999', errors: [], waitForReset: false },
  ])(
    'keeps $name pending with its delay and admits input after recovery',
    async (scenario) => {
      const reset = Math.ceil(Date.now() / 1000) + 120
      let limited = true
      const native = await githubFixture((_query, response) => {
        if (limited) {
          if (scenario.remaining !== undefined)
            response.setHeader('x-ratelimit-remaining', scenario.remaining)
          response.setHeader('x-ratelimit-reset', String(reset))
          json(
            response,
            scenario.status === 200
              ? { data: null, errors: scenario.errors }
              : { message: 'You have exceeded a secondary rate limit.' },
            scenario.status,
          )
        } else json(response, { data: { viewer: { id: 'U_bot', login: 'example[bot]' } } })
      })
      const core = coreFixture()
      const context = contexts()
      let queued = receipt(
        event(
          {
            ...webhook,
            action: 'created',
            comment: nativeComment,
            issue: { ...webhook.pull_request, pull_request: {} },
          },
          'issue_comment',
        ),
      )
      let available = true
      const completions: { state: string; retry_after_ms?: number }[] = []
      const endpoint = await localServer(async (request, response) => {
        const body = await requestBody(request)
        expect(request.headers.authorization).toBe('Bearer local-core-token')
        if (request.url === '/channel-connector/events/claim-next') {
          schemas.zClaimNextChannelConnectorEventRequest.parse(body)
          if (!available) {
            response.writeHead(204)
            response.end()
            return
          }
          available = false
          json(response, z.json().parse(JSON.parse(JSON.stringify(queued))))
        } else if (request.url?.endsWith(`/events/${queued.receipt_id}/complete`)) {
          expect(schemas.zCompleteChannelConnectorEventRequest.safeParse(body).success).toBe(true)
          const completion = z
            .object({
              state: z.string(),
              retry_after_ms: z.number().optional(),
              lease_token: z.string(),
              lease_generation: z.number(),
            })
            .parse(body)
          expect(completion.lease_token).toBe(queued.lease_token)
          expect(completion.lease_generation).toBe(queued.lease_generation)
          completions.push(completion)
          json(response, { receipt_id: queued.receipt_id, state: completion.state })
        } else throw new Error('unexpected Core request')
      })
      const client = new CoreClient({ baseUrl: endpoint, token: 'local-core-token' })
      const consumer = new ReceiptConsumer({
        capabilities: [{ connector_key: 'omnara', provider: 'github' }],
        client,
        behavior: (saved, work) =>
          processGitHubEvent(saved, work, {
            core,
            apiUrl: native.url,
            reserveWorkBytes: context.budget.reserve,
          }),
        workBudget: context.budget,
        maxConcurrentEvents: 1,
        maxAttempts: 3,
        leaseMs: 30_000,
        claimTimeoutMs: 2_000,
        behaviorTimeoutMs: 10_000,
        completionTimeoutMs: 2_000,
        idlePollMs: 10,
        logger: context.factory.logger,
      })
      // Keep one consumer alive across recovery: an aborted HTTP claim handler
      // can outlive run() and consume a replacement lease from this simple fake.
      const stop = new AbortController()
      const running = consumer.run(stop.signal)
      try {
        await vi.waitFor(
          () => {
            expect(completions).toHaveLength(1)
          },
          { timeout: 5_000 },
        )
        expect(completions[0]?.state).toBe('pending')
        if (scenario.waitForReset) {
          expect(completions[0]?.retry_after_ms).toBeGreaterThan(115_000)
          expect(completions[0]?.retry_after_ms).toBeLessThanOrEqual(121_000)
        } else expect(completions[0]?.retry_after_ms).toBe(60_000)
        expect(core.deliverWorkflow).not.toHaveBeenCalled()
        expect(core.publishDefinition).not.toHaveBeenCalled()
        expect(native.calls.filter((call) => call.path === '/graphql')).toHaveLength(1)

        limited = false
        queued = { ...queued, lease_token: randomUUID(), lease_generation: 2, attempt_count: 2 }
        available = true
        await vi.waitFor(
          () => {
            expect(completions).toHaveLength(2)
          },
          { timeout: 5_000 },
        )
        expect(completions[1]?.state).toBe('completed')
        expect(completions[1]?.retry_after_ms).toBeUndefined()
        expect(core.deliverWorkflow).toHaveBeenCalledOnce()
        expect(core.deliverWorkflow.mock.calls[0]?.[1].instance_key).toBe('repo:456:pr:7')
        expect(native.calls.filter((call) => call.path === '/graphql')).toHaveLength(2)
        expect(native.calls.filter((call) => call.path.endsWith('/access_tokens'))).toHaveLength(2)
        expect(mutationInputs(native.calls)).toEqual([])
      } finally {
        stop.abort()
        await running
      }
      await vi.waitFor(() => {
        expect(context.budget.usedBytes).toBe(0)
      })
    },
    15_000,
  )
})
