import type {
  ChannelConnectorEventReceipt,
  ChannelConnectorInputResponse,
  PublishChannelConnectorDefinitionRequest,
} from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import { ReceiptConsumer } from '../consumers/receipt-consumer'
import { WorkByteBudget } from '../work-budget'
import { CoreClient, type ReceiptWorkflowRequest } from './client'
import { initialReceiptWorkBytes, ReceiptClientError } from './receipt-http'

describe('generated receipt workflow client', () => {
  it('publishes setup definitions with app/install IDs and no fabricated event receipt', async () => {
    const scope = {
      integration_app_id: receipt().integration_app_id,
      integration_install_id: receipt().integration_install_id,
    }
    const body: PublishChannelConnectorDefinitionRequest = {
      implementation_key: 'slack_channel',
      kind: 'SLACK_CHANNEL',
      description: 'A Slack channel.',
      send_params_schema: { type: 'object', additionalProperties: false },
      capabilities: {
        read: true,
        send: true,
        text: true,
        artifacts: true,
        permissions: true,
        questions: true,
        creates_reply_channel: true,
      },
    }
    const expected = { ...body, id: 'cdef_aaaaaaaaaaaaaaaaaaaaaaaaae' }
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json(expected))
    expect(
      await workflowClient(fetch).publishDefinition(scope, body, new AbortController().signal),
    ).toEqual(expected)
    const sent = requireRequest(fetch.mock.calls[0]?.[0])
    expect(sent.url).toContain(
      `/apps/${scope.integration_app_id}/installations/${scope.integration_install_id}/channel-definitions/publish`,
    )
    expect(await sent.json()).toEqual(body)
    expect(sent.headers.get('authorization')).toBe('Bearer private-workflow-token')
    expect(sent.redirect).toBe('error')
  })

  it('preserves only definite admission denial without retaining provider diagnostics', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
      Response.json(
        {
          code: 'managed_work_admission_denied',
          error: 'private-detail-never-exposed',
        },
        { status: 409 },
      ),
    )
    const result = await workflowClient(fetch)
      .deliverWorkflow(receipt(), workflowRequest, new AbortController().signal)
      .catch((cause: unknown) => cause)
    expect(result).toMatchObject({
      code: 'http_error',
      status: 409,
      apiCode: 'managed_work_admission_denied',
    })
    expect(String(result)).not.toContain('private-detail-never-exposed')
    expect(fetch).toHaveBeenCalledOnce()
  })

  it('scopes lookup to the queued connection and accepts only requested input keys', async () => {
    const body = {
      route_id: workflowRequest.route_id,
      instance_key: 'thread-1',
      input_keys: ['message-1'],
    }
    const expected = { exists: true, agent_state: 'active', input_keys: ['message-1'] }
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json(expected))
    expect(
      await workflowClient(fetch).lookupWorkflow(receipt(), body, new AbortController().signal),
    ).toEqual(expected)
    const sent = requireRequest(fetch.mock.calls[0]?.[0])
    expect(sent.url).toContain(
      `/installations/${receipt().integration_install_id}/workflows/lookup`,
    )
    expect(await sent.json()).toEqual(body)
    expect(sent.redirect).toBe('error')
  })

  it.each([
    { exists: false, input_keys: ['message-1'] },
    { exists: true, input_keys: [] },
    { exists: true, agent_state: 'active', input_keys: ['unrequested'] },
    { exists: false, agent_state: 'active', input_keys: [] },
  ])('rejects inconsistent lookup state before behavior can use it', async (result) => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json(result))
    await expect(
      workflowClient(fetch).lookupWorkflow(
        receipt(),
        {
          route_id: workflowRequest.route_id,
          instance_key: 'thread-1',
          input_keys: ['message-1'],
        },
        new AbortController().signal,
      ),
    ).rejects.toMatchObject({ code: 'invalid_response' })
  })

  it('uses the original queued authority even if a runtime caller passes replacement proof', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json(workflowResult))
    const client = workflowClient(fetch)
    const original = receipt()
    const request = {
      ...workflowRequest,
      receipt: { receipt_id: 'forged', lease_token: 'forged', lease_generation: 999 },
    }
    expect(await client.deliverWorkflow(original, request, new AbortController().signal)).toEqual(
      workflowResult,
    )
    const sent = requireRequest(fetch.mock.calls[0]?.[0])
    expect(sent.url).toBe(
      `https://core.example.test/api/v1/channel-connector/apps/${original.integration_app_id}/installations/${original.integration_install_id}/workflows/deliver`,
    )
    expect(sent.redirect).toBe('error')
    expect(sent.headers.get('authorization')).toBe('Bearer private-workflow-token')
    expect(await sent.json()).toEqual({
      ...workflowRequest,
      receipt: {
        receipt_id: original.receipt_id,
        lease_token: original.lease_token,
        lease_generation: original.lease_generation,
      },
    })
  })

  it.each([
    new TypeError('private-workflow-token'),
    Response.json({ error: 'private-workflow-token' }, { status: 503 }),
  ])('does not retry ambiguous workflow delivery or expose remote diagnostics', async (failure) => {
    const fetch = vi.fn<typeof globalThis.fetch>(() =>
      failure instanceof Error ? Promise.reject(failure) : Promise.resolve(failure),
    )
    const result = await workflowClient(fetch)
      .deliverWorkflow(receipt(), workflowRequest, new AbortController().signal)
      .catch((cause: unknown) => cause)
    expect(result).toBeInstanceOf(ReceiptClientError)
    expect(String(result)).not.toContain('private-workflow-token')
    expect(fetch).toHaveBeenCalledOnce()
  })

  it.each([
    Response.json(workflowResult, { status: 202 }),
    Response.json({ ...workflowResult, channel_id: 'unvalidated-channel' }),
  ])('rejects nonterminal or malformed workflow success', async (response) => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(response)
    await expect(
      workflowClient(fetch).deliverWorkflow(
        receipt(),
        workflowRequest,
        new AbortController().signal,
      ),
    ).rejects.toMatchObject({ code: 'invalid_response' })
    expect(fetch).toHaveBeenCalledOnce()
  })

  it('rejects pre-cancellation and imprecise lease generations before HTTP dispatch', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>()
    const client = workflowClient(fetch)
    const controller = new AbortController()
    controller.abort()
    await expect(
      client.deliverWorkflow(receipt(), workflowRequest, controller.signal),
    ).rejects.toMatchObject({ code: 'aborted' })
    await expect(
      client.deliverWorkflow(
        { ...receipt(), lease_generation: Number.MAX_SAFE_INTEGER + 1 },
        workflowRequest,
        new AbortController().signal,
      ),
    ).rejects.toMatchObject({ code: 'invalid_request' })
    expect(fetch).not.toHaveBeenCalled()
  })

  it('aborts the actual in-flight workflow request', async () => {
    let started!: () => void
    const dispatched = new Promise<void>((resolve) => {
      started = resolve
    })
    const fetch = vi.fn<typeof globalThis.fetch>((input) => {
      const request = requireRequest(input)
      started()
      return new Promise((_resolve, reject) => {
        request.signal.addEventListener(
          'abort',
          () => {
            reject(new Error('private-workflow-token'))
          },
          { once: true },
        )
      })
    })
    const controller = new AbortController()
    const pending = workflowClient(fetch).deliverWorkflow(
      receipt(),
      workflowRequest,
      controller.signal,
    )
    const rejected = expect(pending).rejects.toMatchObject({ code: 'aborted' })
    await dispatched
    controller.abort()
    await rejected
    expect(fetch).toHaveBeenCalledOnce()
  })

  it('executes a typed behavior workflow and then completes that same receipt lease', async () => {
    const controller = new AbortController()
    const claimed = receipt()
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(Response.json(claimed))
      .mockResolvedValueOnce(Response.json(workflowResult))
      .mockImplementationOnce(() => {
        controller.abort()
        return Promise.resolve(
          Response.json({ receipt_id: claimed.receipt_id, state: 'completed' }),
        )
      })
    const client = workflowClient(fetch)
    const workBudget = new WorkByteBudget(initialReceiptWorkBytes)
    const behavior = vi.fn(
      async (
        original: Readonly<ChannelConnectorEventReceipt>,
        { signal }: { signal: AbortSignal },
      ) => {
        expect(original).toEqual(claimed)
        await client.deliverWorkflow(original, workflowRequest, signal)
      },
    )
    await new ReceiptConsumer({
      capabilities: [{ connector_key: 'fixture', provider: 'fixture' }],
      client,
      behavior,
      workBudget,
      maxConcurrentEvents: 1,
      maxAttempts: 3,
      leaseMs: 1000,
      claimTimeoutMs: 100,
      behaviorTimeoutMs: 200,
      completionTimeoutMs: 100,
      idlePollMs: 10,
      logger: { error: vi.fn() },
    }).run(controller.signal)
    expect(behavior).toHaveBeenCalledOnce()
    expect(fetch).toHaveBeenCalledTimes(3)
    const workflow = requireRequest(fetch.mock.calls[1]?.[0])
    const completion = requireRequest(fetch.mock.calls[2]?.[0])
    expect(await workflow.json()).toMatchObject({
      receipt: {
        receipt_id: claimed.receipt_id,
        lease_token: claimed.lease_token,
        lease_generation: claimed.lease_generation,
      },
    })
    expect(await completion.json()).toEqual({
      state: 'completed',
      lease_token: claimed.lease_token,
      lease_generation: claimed.lease_generation,
    })
    expect(workBudget.usedBytes).toBe(0)
  })
})

function requireRequest(input: Parameters<typeof globalThis.fetch>[0] | undefined): Request {
  if (!(input instanceof Request)) throw new Error('expected generated SDK Request')
  return input
}

function workflowClient(fetch: typeof globalThis.fetch) {
  return new CoreClient({
    baseUrl: 'https://core.example.test/api/v1',
    token: 'private-workflow-token',
    fetch,
  })
}

function receipt(): ChannelConnectorEventReceipt {
  return {
    receipt_id: 'irec_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    integration_install_id: 'iin_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    event_id: 'event-1',
    payload: { lease_token: 'payload-is-not-authority' },
    state: 'processing',
    lease_token: '01994550-1234-7123-8123-123456789abc',
    lease_generation: 3,
    lease_expires_at: new Date(Date.now() + 1000).toISOString(),
    attempt_count: 1,
    last_error: {},
  }
}

const workflowRequest: ReceiptWorkflowRequest = {
  route_id: 'iroute_aaaaaaaaaaaaaaaaaaaaaaaaaa',
  instance_key: 'thread-1',
  input_key: 'message-1',
  target: {
    definition_id: 'cdef_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    provider_ref: 'thread-1',
    provider_ref_kind: 'thread',
  },
  grants: { read: true, send: true },
  author: { ref: 'author-1', display_name: 'Author' },
  content_blocks: [{ type: 'text', text: 'verified event' }],
}

const workflowResult: ChannelConnectorInputResponse = {
  agent_id: 'agt_aaaaaaaaaaaaaaaaaaaaaaaaaa',
  channel_id: 'itgt_aaaaaaaaaaaaaaaaaaaaaaaaaa',
  binding_id: 'ibnd_aaaaaaaaaaaaaaaaaaaaaaaaaa',
  agent_input_id: 'ain_aaaaaaaaaaaaaaaaaaaaaaaaaa',
  created_agent: true,
  created_input: true,
  content_blocks: [{ type: 'text', text: 'verified event' }],
}
