import type { ChannelConnectorEventReceipt, ChannelConnectorInputResponse } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import { CoreClient, type ReceiptInputRequest } from './client'

const id = 'aaaaaaaaaaaaaaaaaaaaaaaaae'
const receipt: ChannelConnectorEventReceipt = {
  receipt_id: `irec_${id}`,
  integration_app_id: `iapp_${id}`,
  integration_install_id: `iin_${id}`,
  event_id: 'Ev1',
  payload: {},
  state: 'processing',
  lease_token: '01994550-1234-7123-8123-123456789abc',
  lease_generation: 3,
  lease_expires_at: '2099-01-01T00:00:00Z',
  attempt_count: 1,
  last_error: {},
}
const query = { provider_ref: 'C1:111.222', input_keys: ['message-1'], cursor: 'page-2', limit: 2 }
const recipient = { agent_id: `agt_${id}`, binding_id: `ibnd_${id}`, input_keys: ['message-1'] }
const lookupResult = {
  channel_id: `itgt_${id}`,
  has_receive_binding_history: true,
  workflow_started: false,
  recipients: [recipient],
  next_cursor: null,
}
const input: ReceiptInputRequest = {
  binding_id: recipient.binding_id,
  input_key: 'message-1',
  author: { ref: 'U1', display_name: 'Alice' },
  content_blocks: [{ type: 'text', text: 'hello' }],
  input_precondition: { input_key: 'files-1', exists: false },
}
const inputResult: ChannelConnectorInputResponse = {
  agent_id: recipient.agent_id,
  channel_id: lookupResult.channel_id,
  binding_id: recipient.binding_id,
  agent_input_id: `ain_${id}`,
  created_agent: false,
  created_input: true,
  content_blocks: [],
}
function client(fetch: typeof globalThis.fetch) {
  return new CoreClient({
    baseUrl: 'https://core.example.test/api/v1',
    token: 'private-token',
    fetch,
  })
}
function request(value: Parameters<typeof globalThis.fetch>[0] | undefined): Request {
  if (!(value instanceof Request)) throw new Error('expected generated request')
  return value
}

describe('generated receipt recipient client', () => {
  it('preserves containment for an existing channel with no receive history or recipients', async () => {
    const expected = {
      ...lookupResult,
      parent_channel_id: `itgt_aaaaaaaaaaaaaaaaaaaaaaaabi`,
      has_receive_binding_history: false,
      recipients: [],
    }
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json(expected))
    expect(
      await client(fetch).lookupRecipients(receipt, query, new AbortController().signal),
    ).toEqual(expected)
  })

  it('preserves false receive history when a recipient appears between core reads', async () => {
    const expected = { ...lookupResult, has_receive_binding_history: false }
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json(expected))
    const observed = await client(fetch).lookupRecipients(
      receipt,
      query,
      new AbortController().signal,
    )
    expect(observed).toEqual(expected)
    expect(observed.has_receive_binding_history).toBe(false)
    expect(observed.recipients).toEqual([recipient])
    expect(fetch).toHaveBeenCalledOnce()
  })

  it('injects original receipt proof and connection scope into lookup and delivery', async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(Response.json(lookupResult))
      .mockResolvedValueOnce(Response.json(inputResult))
    const core = client(fetch)
    const replacement = { receipt_id: 'forged', lease_token: 'forged', lease_generation: 999 }
    const forgedQuery = { ...query, receipt: replacement }
    const forgedInput = { ...input, receipt: replacement }
    expect(await core.lookupRecipients(receipt, forgedQuery, new AbortController().signal)).toEqual(
      lookupResult,
    )
    expect(await core.deliverInput(receipt, forgedInput, new AbortController().signal)).toEqual(
      inputResult,
    )
    for (const [index, body, path] of [
      [0, query, 'channels/recipients'],
      [1, input, 'channels/deliver'],
    ] as const) {
      const sent = request(fetch.mock.calls[index]?.[0])
      expect(sent.url).toBe(
        `https://core.example.test/api/v1/channel-connector/apps/${receipt.integration_app_id}/installations/${receipt.integration_install_id}/${path}`,
      )
      expect(sent.redirect).toBe('error')
      expect(sent.headers.get('authorization')).toBe('Bearer private-token')
      expect(await sent.json()).toEqual({
        ...body,
        receipt: {
          receipt_id: receipt.receipt_id,
          lease_token: receipt.lease_token,
          lease_generation: receipt.lease_generation,
        },
      })
    }
  })

  it.each([
    { ...lookupResult, recipients: [{ ...recipient, input_keys: ['unrequested'] }] },
    { ...lookupResult, recipients: [recipient, recipient] },
    { ...lookupResult, channel_id: undefined },
    { ...lookupResult, next_cursor: 123 },
    {
      ...lookupResult,
      channel_id: undefined,
      parent_channel_id: `itgt_${id}`,
      has_receive_binding_history: false,
      recipients: [],
    },
  ])('rejects inconsistent recipient state before rendering', async (body) => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json(body))
    await expect(
      client(fetch).lookupRecipients(receipt, query, new AbortController().signal),
    ).rejects.toMatchObject({ code: 'invalid_response' })
  })

  it('validates lookup limits before HTTP and bounds the returned page size', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>()
    const core = client(fetch)
    await expect(
      core.lookupRecipients(receipt, { ...query, limit: 101 }, new AbortController().signal),
    ).rejects.toMatchObject({ code: 'invalid_request' })
    expect(fetch).not.toHaveBeenCalled()
    fetch.mockResolvedValue(
      Response.json({
        ...lookupResult,
        recipients: [
          recipient,
          {
            agent_id: 'agt_aaaaaaaaaaaaaaaaaaaaaaaabi',
            binding_id: 'ibnd_aaaaaaaaaaaaaaaaaaaaaaaabi',
            input_keys: [],
          },
        ],
      }),
    )
    await expect(
      core.lookupRecipients(receipt, { ...query, limit: 1 }, new AbortController().signal),
    ).rejects.toMatchObject({ code: 'invalid_response' })
  })

  it.each(['lookupRecipients', 'deliverInput'] as const)(
    'does not retry %s conflicts or accept imprecise leases',
    async (method) => {
      const fetch = vi
        .fn<typeof globalThis.fetch>()
        .mockResolvedValue(Response.json({ code: 'state_transition_conflict' }, { status: 409 }))
      const core = client(fetch)
      const body = { ...query, ...input }
      await expect(core[method](receipt, body, new AbortController().signal)).rejects.toMatchObject(
        { code: 'http_error', status: 409 },
      )
      expect(fetch).toHaveBeenCalledOnce()
      await expect(
        core[method](
          { ...receipt, lease_generation: Number.MAX_SAFE_INTEGER + 1 },
          body,
          new AbortController().signal,
        ),
      ).rejects.toMatchObject({ code: 'invalid_request' })
      expect(fetch).toHaveBeenCalledOnce()
    },
  )

  it.each(['lookupRecipients', 'deliverInput'] as const)(
    'aborts the actual %s request',
    async (method) => {
      let started!: () => void
      const dispatched = new Promise<void>((resolve) => {
        started = resolve
      })
      const fetch = vi.fn<typeof globalThis.fetch>((input) => {
        const sent = request(input)
        started()
        return new Promise((_resolve, reject) => {
          sent.signal.addEventListener(
            'abort',
            () => {
              reject(new Error('private-token'))
            },
            { once: true },
          )
        })
      })
      const controller = new AbortController()
      const pending = expect(
        client(fetch)[method](receipt, { ...query, ...input }, controller.signal),
      ).rejects.toMatchObject({ code: 'aborted' })
      await dispatched
      controller.abort()
      await pending
      expect(fetch).toHaveBeenCalledOnce()
    },
  )
})
