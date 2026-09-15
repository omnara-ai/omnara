import type {
  ChannelConnectorRecipient,
  LookupChannelConnectorRecipientsResponse,
} from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import { ReceiptClientError } from '../receipt-http'
import { processSlackEvent } from './behavior'
import { filesKey, fixture, id, plainKey, receipt, result } from './behavior-test-support'

const secondID = 'aaaaaaaaaaaaaaaaaaaaaaaabi'
const recipient: ChannelConnectorRecipient = {
  agent_id: `agt_${id}`,
  binding_id: `ibnd_${id}`,
  input_keys: [],
}
const secondRecipient: ChannelConnectorRecipient = {
  agent_id: `agt_${secondID}`,
  binding_id: `ibnd_${secondID}`,
  input_keys: [],
}
function page(
  recipients: ChannelConnectorRecipient[] = [recipient],
): LookupChannelConnectorRecipientsResponse {
  return {
    channel_id: `itgt_${id}`,
    has_receive_binding_history: true,
    workflow_started: false,
    recipients,
    next_cursor: null,
  }
}
function reply() {
  return receipt({ type: 'message', text: 'continue', thread_ts: '99.000001' })
}

describe('Slack bound recipient admission', () => {
  it('launches a mention normally at an address with only a send binding', async () => {
    const { context } = await fixture()
    context.lookupRecipients.mockResolvedValue({
      channel_id: `itgt_${id}`,
      has_receive_binding_history: false,
      workflow_started: false,
      recipients: [],
      next_cursor: null,
    })
    context.deliverWorkflow.mockResolvedValue({
      ...result,
      agent_id: secondRecipient.agent_id,
      created_agent: true,
    })
    const queued = receipt()
    await processSlackEvent(queued, context)
    expect(context.listRoutes).toHaveBeenCalledOnce()
    expect(context.lookupWorkflow).toHaveBeenCalledOnce()
    expect(context.deliverWorkflow).toHaveBeenCalledOnce()
    expect(context.deliverWorkflow.mock.calls[0]?.[1]).toMatchObject({
      route_id: `iroute_${id}`,
      instance_key: 'C1:100.000002',
      only_if_unbound: true,
      input_key: plainKey,
      target: { provider_ref: 'C1:100.000002' },
    })
    // Only core's configured workflow selects the new agent. An existing sender
    // receives no direct input and supplies no launch identity or receive grant.
    expect(context.deliverInput).not.toHaveBeenCalled()
    expect(context.deliverWorkflow.mock.calls[0]?.[1]).not.toHaveProperty('agent_id')
    expect(context.deliverWorkflow.mock.calls[0]?.[1]).not.toHaveProperty('binding_id')
  })

  it('delivers a cron-created thread reply with zero launch routes and no history read', async () => {
    const { context, calls } = await fixture()
    context.listRoutes.mockResolvedValue([])
    context.lookupRecipients.mockResolvedValue(page())
    const queued = reply()
    const original = JSON.stringify(queued)
    await processSlackEvent(queued, context)
    expect(JSON.stringify(queued)).toBe(original)
    expect(context.lookupRecipients).toHaveBeenCalledWith(
      queued,
      {
        provider_ref: 'C1:99.000001',
        input_keys: [plainKey, filesKey],
      },
      expect.any(AbortSignal),
    )
    expect(context.deliverInput).toHaveBeenCalledOnce()
    const input = context.deliverInput.mock.calls[0]?.[1]
    expect(input).toMatchObject({
      binding_id: recipient.binding_id,
      input_key: plainKey,
      input_precondition: { input_key: filesKey, exists: false },
      delivery_mode: 'steering',
      cancel_open_interactions: true,
      metadata: { history_status: 'skipped' },
    })
    expect(input?.content_blocks).toContainEqual(expect.objectContaining({ text: 'continue' }))
    expect(input).not.toHaveProperty('grants')
    expect(input).not.toHaveProperty('target')
    expect(context.listRoutes).not.toHaveBeenCalled()
    expect(context.lookupWorkflow).not.toHaveBeenCalled()
    expect(context.deliverWorkflow).not.toHaveBeenCalled()
    expect(context.publishDefinition).not.toHaveBeenCalled()
    expect(calls).not.toContain('/conversations.history')
    expect(calls).not.toContain('/conversations.replies')
    expect(calls).toContain('/reactions.add')

    context.lookupRecipients.mockResolvedValue(page([{ ...recipient, input_keys: [plainKey] }]))
    context.deliverInput.mockResolvedValue({
      ...result,
      created_agent: false,
      created_input: false,
    })
    calls.length = 0
    await processSlackEvent(queued, context)
    expect(context.deliverInput.mock.calls[1]?.[1].input_precondition).toBeUndefined()
    expect(calls).toEqual([])
  })

  it('does not launch when revoked or archived receive binding history has no live recipients', async () => {
    const { context, calls } = await fixture()
    context.lookupRecipients.mockResolvedValue(page([]))
    await processSlackEvent(receipt(), context)
    expect(context.listRoutes).not.toHaveBeenCalled()
    expect(context.deliverInput).not.toHaveBeenCalled()
    expect(context.deliverWorkflow).not.toHaveBeenCalled()
    expect(calls).toEqual([])
  })

  it('paginates recipients with independent semantic keys and shares one media preparation', async () => {
    const { context, calls, work } = await fixture((request, response) => {
      if (request.url !== '/file') return false
      expect(context.lookupRecipients).toHaveBeenCalled()
      response.end('file content')
      return true
    })
    context.lookupRecipients
      .mockResolvedValueOnce({
        ...page([{ ...recipient, input_keys: [plainKey] }]),
        next_cursor: 'page-2',
      })
      .mockResolvedValueOnce(page([secondRecipient]))
    await processSlackEvent(
      receipt({
        files: [{ name: 'notes.txt', url_private: `${context.apiUrl}/file` }],
      }),
      context,
    )
    expect(context.lookupRecipients.mock.calls[0]?.[1]).not.toHaveProperty('cursor')
    expect(context.lookupRecipients.mock.calls[1]?.[1].cursor).toBe('page-2')
    const first = context.deliverInput.mock.calls[0]?.[1]
    const second = context.deliverInput.mock.calls[1]?.[1]
    expect(first?.input_precondition).toEqual({ input_key: plainKey, exists: true })
    expect(second?.input_precondition).toEqual({ input_key: plainKey, exists: false })
    expect(first?.content_blocks[1]).toMatchObject({
      text: 'Files for the previous Slack message.',
      metadata: { omnara_hidden: 'true' },
    })
    expect(second?.content_blocks[1]).toHaveProperty('metadata.omnara_display_text')
    expect(first?.content_blocks.at(-1)).toBe(second?.content_blocks.at(-1))
    expect(calls.filter((path) => path === '/file')).toHaveLength(1)
    expect(work.release).toHaveBeenCalledOnce()
    expect(context.listRoutes).not.toHaveBeenCalled()
  })

  it('resumes partial workflow fanout even though its first route created receive binding history', async () => {
    const { context } = await fixture()
    context.lookupRecipients.mockResolvedValue({ ...page(), workflow_started: true })
    context.listRoutes.mockResolvedValue([
      { route_id: `iroute_${id}`, grants: { read: true, send: true } },
      { route_id: `iroute_${secondID}`, grants: { read: true, send: true } },
    ])
    context.lookupWorkflow
      .mockResolvedValueOnce({ exists: true, agent_state: 'active', input_keys: [plainKey] })
      .mockResolvedValueOnce({ exists: false, input_keys: [] })
    await processSlackEvent(receipt(), context)
    expect(context.deliverInput).not.toHaveBeenCalled()
    expect(context.deliverWorkflow).toHaveBeenCalledTimes(2)
    expect(context.deliverWorkflow.mock.calls[0]?.[1].only_if_unbound).toBeUndefined()
    expect(context.deliverWorkflow.mock.calls[0]?.[1].input_precondition).toBeUndefined()
    expect(context.deliverWorkflow.mock.calls[1]?.[1].only_if_unbound).toBe(true)
  })

  it('reselects a binding that appears during initial workflow admission', async () => {
    const { context, calls, work } = await fixture((request, response) => {
      if (request.url !== '/file') return false
      response.end('file content')
      return true
    })
    context.lookupRecipients
      .mockResolvedValueOnce({
        has_receive_binding_history: false,
        workflow_started: false,
        recipients: [],
        next_cursor: null,
      })
      .mockResolvedValueOnce(page())
    context.deliverWorkflow.mockRejectedValueOnce(new ReceiptClientError('http_error', 409))
    await processSlackEvent(
      receipt({
        files: [{ name: 'notes.txt', url_private: `${context.apiUrl}/file` }],
      }),
      context,
    )
    expect(context.deliverWorkflow).toHaveBeenCalledOnce()
    expect(context.deliverWorkflow.mock.calls[0]?.[1].only_if_unbound).toBe(true)
    expect(context.deliverInput).toHaveBeenCalledOnce()
    expect(context.deliverInput.mock.calls[0]?.[1].metadata?.history_status).toBe('skipped')
    expect(context.listRoutes).toHaveBeenCalledOnce()
    expect(calls.filter((path) => path === '/file')).toHaveLength(1)
    expect(work.release).toHaveBeenCalledOnce()
  })

  it('rerenders sibling conflicts through fresh global recipient lookup', async () => {
    const { context } = await fixture()
    context.lookupRecipients
      .mockResolvedValueOnce(page())
      .mockResolvedValueOnce(page([{ ...recipient, input_keys: [filesKey] }]))
    context.deliverInput.mockRejectedValueOnce(new ReceiptClientError('http_error', 409))
    await processSlackEvent(reply(), context)
    expect(context.deliverInput.mock.calls[0]?.[1].input_key).toBe(plainKey)
    expect(context.deliverInput.mock.calls[1]?.[1].input_key).toBe(filesKey)
    expect(context.deliverInput.mock.calls[1]?.[1].input_precondition).toBeUndefined()
    expect(context.listRoutes).not.toHaveBeenCalled()
  })

  it.each([404, 409])(
    'reselects a revoked recipient after HTTP %s without replacement launch',
    async (status) => {
      const { context } = await fixture()
      context.lookupRecipients.mockResolvedValueOnce(page()).mockResolvedValueOnce(page([]))
      context.deliverInput.mockRejectedValueOnce(new ReceiptClientError('http_error', status))
      await processSlackEvent(reply(), context)
      expect(context.deliverInput).toHaveBeenCalledOnce()
      expect(context.listRoutes).not.toHaveBeenCalled()
      expect(context.deliverWorkflow).not.toHaveBeenCalled()
    },
  )

  it('stops unchanged conflicts and cyclic pagination without duplicate dispatch', async () => {
    const { context } = await fixture()
    const conflict = new ReceiptClientError('http_error', 409)
    context.lookupRecipients.mockResolvedValue(page())
    context.deliverInput.mockRejectedValue(conflict)
    await expect(processSlackEvent(reply(), context)).rejects.toBe(conflict)
    expect(context.deliverInput).toHaveBeenCalledOnce()
    expect(context.lookupRecipients).toHaveBeenCalledTimes(2)
    context.deliverInput.mockReset().mockResolvedValue({ ...result, created_input: false })
    context.lookupRecipients.mockReset().mockResolvedValue({ ...page(), next_cursor: 'cycle' })
    await expect(processSlackEvent(reply(), context)).rejects.toMatchObject({
      code: 'invalid_response',
    })
    expect(context.deliverInput).toHaveBeenCalledOnce()
    expect(context.lookupRecipients).toHaveBeenCalledTimes(2)
  })
})
