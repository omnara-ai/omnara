import { randomUUID } from 'node:crypto'

import { type ChannelConnectorEventReceipt, schemas } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'
import { z } from 'zod'

import { ReceiptConsumer } from '../consumers/receipt-consumer'
import { CoreClient } from '../core/client'
import { createGitHubFactory } from './factory'
import {
  app,
  contexts,
  event,
  installation,
  nativeComment,
  nativeFixture,
  receipt,
  signedRequest,
  suffix,
  webhook,
} from './inbound-test-support'
import { json, localServer, mutationInputs, oldCommit, requestBody } from './test-support'

/** Real generated SDK/HTTP and receipt consumer; only durable Core state and
 * GitHub's network responses are faked here. PostgreSQL admission is covered by
 * the core workflow integration suite, not reimplemented as a gateway guarantee.
 */
describe('GitHub local HTTP provider journey', () => {
  it('accepts signed events, claims durable receipts, reuses the PR workflow, and completes exact leases including semantic replay', async () => {
    const native = await nativeFixture()
    const context = contexts()
    const pending: ChannelConnectorEventReceipt[] = []
    const claimed = new Map<string, ChannelConnectorEventReceipt>()
    const completed: string[] = []
    const inputs = new Set<string>()
    const delivered: ReturnType<typeof schemas.zDeliverChannelConnectorWorkflowRequest.parse>[] = []
    const prefix = `/channel-connector/apps/${app.app.id}`
    const installPrefix = `${prefix}/installations/${installation.install.id}`
    const violations: string[] = []
    let workflowCreated = false
    let agentCreations = 0
    const baseUrl = await localServer(async (request, response) => {
      if (request.headers.authorization !== 'Bearer local-core-token')
        violations.push('missing core auth')
      const url = new URL(request.url ?? '', 'http://localhost')
      const body = request.method === 'POST' ? await requestBody(request) : undefined
      if (url.pathname === `${prefix}/configuration`) json(response, app)
      else if (
        url.pathname === `${installPrefix}/configuration` ||
        url.pathname === `${prefix}/installations/resolve`
      )
        json(response, installation)
      else if (url.pathname === `${prefix}/events`) {
        const saved = schemas.zChannelInboundEventRequest.parse(body)
        // Distinct receipt IDs in the same exact project/installation scope.
        const id = `irec_${String.fromCharCode(97 + pending.length).repeat(26)}`
        pending.push({
          ...receipt(event()),
          receipt_id: id,
          event_id: saved.event_id,
          payload: saved.payload,
        })
        json(response, { receipt_id: id, state: 'pending' }, 202)
      } else if (url.pathname === '/channel-connector/events/claim-next') {
        schemas.zClaimNextChannelConnectorEventRequest.parse(body)
        const next = pending.find((row) => !claimed.has(row.receipt_id))
        if (!next) {
          response.writeHead(204)
          response.end()
          return
        }
        claimed.set(next.receipt_id, next)
        json(response, z.json().parse(next))
      } else if (url.pathname === `${installPrefix}/routes`)
        json(response, {
          routes: [{ id: `iroute_${suffix}`, behavior_key: 'github_pr', configuration: {} }],
          next_cursor: null,
        })
      else if (url.pathname === `${installPrefix}/workflows/lookup`) {
        const query = schemas.zLookupChannelConnectorWorkflowRequest.parse(body)
        expect(query.instance_key).toBe('repo:456:pr:7')
        json(
          response,
          workflowCreated
            ? {
                exists: true,
                agent_state: 'active',
                input_keys: query.input_keys.filter((key) => inputs.has(key)),
              }
            : { exists: false, input_keys: [] },
        )
      } else if (url.pathname === `${installPrefix}/channel-definitions/publish`) {
        const definition = schemas.zPublishChannelConnectorDefinitionRequest.parse(body)
        json(
          response,
          z.json().parse({
            ...definition,
            id: `cdef_${definition.kind === 'GITHUB_PR' ? suffix : 'bbbbbbbbbbbbbbbbbbbbbbbbbb'}`,
          }),
        )
      } else if (url.pathname === `${installPrefix}/workflows/deliver`) {
        const input = schemas.zDeliverChannelConnectorWorkflowRequest.parse(body)
        const owner = claimed.get(input.receipt.receipt_id)
        expect(input.receipt.lease_token).toBe(owner?.lease_token)
        expect(Number(input.receipt.lease_generation)).toBe(owner?.lease_generation)
        expect(input.instance_key).toBe('repo:456:pr:7')
        if (!workflowCreated) agentCreations += 1
        const created = !workflowCreated
        workflowCreated = true
        inputs.add(input.input_key)
        delivered.push(input)
        json(response, {
          agent_id: `agt_${suffix}`,
          channel_id: `itgt_${suffix}`,
          agent_input_id: `ain_${suffix}`,
          created_agent: created,
          created_input: true,
          content_blocks: [],
        })
      } else if (url.pathname.endsWith('/complete')) {
        const completion = schemas.zCompleteChannelConnectorEventRequest.parse(body)
        const id = url.pathname.split('/').at(-2) ?? ''
        expect(completion.lease_token).toBe(claimed.get(id)?.lease_token)
        expect(Number(completion.lease_generation)).toBe(claimed.get(id)?.lease_generation)
        expect(completion.state).toBe('completed')
        completed.push(id)
        json(response, { receipt_id: id, state: completion.state })
      } else {
        violations.push(`unexpected ${request.method} ${url.pathname}`)
        json(response, {}, 404)
      }
    })
    const core = new CoreClient({
      baseUrl,
      token: 'local-core-token',
      requestTimeoutMs: 2000,
      random: () => 0,
    })
    const runtime = await createGitHubFactory({ core, apiUrl: native.url }).create(context.factory)
    const intake = {
      ...context.intake,
      submitInbound: (saved: Parameters<CoreClient['submitInbound']>[1], signal?: AbortSignal) =>
        core.submitInbound(app.app.id, saved, signal),
    }
    const comment = { ...webhook, action: 'created', comment: nativeComment }
    const events = [
      ['pull_request', webhook],
      [
        'pull_request',
        { ...webhook, action: 'synchronize', before: oldCommit, after: 'b'.repeat(40) },
      ],
      ['pull_request_review_comment', comment],
      ['pull_request_review_comment', comment],
    ] as const
    for (const [kind, body] of events)
      expect(
        (
          await runtime.handleWebhook(
            signedRequest(JSON.stringify(body), kind, randomUUID()),
            intake,
          )
        ).status,
      ).toBe(202)
    expect(pending).toHaveLength(4)
    expect(delivered).toEqual([])
    if (!runtime.processReceipt) throw new Error('missing GitHub receipt behavior')
    const consumer = new ReceiptConsumer({
      capabilities: [{ connector_key: 'omnara', provider: 'github' }],
      client: core,
      behavior: runtime.processReceipt,
      workBudget: context.budget,
      maxConcurrentEvents: 1,
      maxAttempts: 3,
      leaseMs: 30_000,
      claimTimeoutMs: 2000,
      behaviorTimeoutMs: 10_000,
      completionTimeoutMs: 2000,
      idlePollMs: 10,
      logger: context.factory.logger,
      random: () => 0,
    })
    const stop = new AbortController()
    const running = consumer.run(stop.signal)
    try {
      await vi.waitFor(
        () => {
          expect(completed).toHaveLength(4)
        },
        { timeout: 15_000 },
      )
    } finally {
      stop.abort()
      await running
      await runtime.close()
    }
    expect(agentCreations).toBe(1)
    expect(delivered).toHaveLength(3)
    expect(delivered[2]?.target).toMatchObject({
      provider_ref: 'repo:456:pr:7:comment:PRRC_1',
      parent: { provider_ref: 'repo:456:pr:7' },
    })
    const block = delivered[2]?.content_blocks[0]
    if (block?.type !== 'text') throw new Error('missing text input')
    expect(block.text).toContain(nativeComment.body)
    expect(violations).toEqual([])
    expect(mutationInputs(native.calls)).toEqual([])
    expect(context.budget.usedBytes).toBe(0)
  }, 20_000)
})
