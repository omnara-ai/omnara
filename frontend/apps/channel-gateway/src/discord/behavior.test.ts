import { describe, expect, it } from 'vitest'

import { ReceiptClientError } from '../receipt-http'
import { ReceiptBehaviorError } from '../types'
import { processDiscordEvent } from './behavior'
import { delivery, event, fixture, inputKey, receipt } from './behavior-test-support'
import { config, json, room, suffix, thread } from './test-support'

describe('Discord receipt behavior', () => {
  it('creates one public thread from a mention and admits the original message through the configured route', async () => {
    const f = await fixture()
    await processDiscordEvent(receipt(), f.context)
    expect(f.calls.filter((call) => call.startsWith('POST'))).toEqual([
      `POST /channels/${room.id}/messages/${event.id}/threads`,
    ])
    expect(f.core.lookupRecipients.mock.calls[0]?.[1]).toEqual({
      provider_ref: event.id,
      input_keys: [inputKey],
    })
    expect(f.core.deliverWorkflow).toHaveBeenCalledOnce()
    expect(f.core.deliverWorkflow.mock.calls[0]?.[1]).toMatchObject({
      instance_key: event.id,
      input_key: inputKey,
      only_if_unbound: true,
      grants: { read: true, send: true },
      author: { ref: event.author.id, display_name: 'Ada Lovelace' },
      target: {
        provider_ref: event.id,
        provider_ref_kind: 'thread',
        display_name: thread.name,
        parent: { provider_ref: room.id, provider_ref_kind: 'channel', display_name: room.name },
      },
      content_blocks: [
        { type: 'text', metadata: { omnara_hidden: 'true' } },
        { type: 'text', text: event.content },
      ],
    })
    expect(f.budget.usedBytes).toBe(0)
  })

  it('continues a cron-created reply thread through its live binding even with no routes', async () => {
    const f = await fixture()
    f.existingThread()
    f.core.listRoutes.mockResolvedValue([])
    f.core.lookupRecipients.mockResolvedValue({
      channel_id: delivery.channel_id,
      has_receive_binding_history: true,
      workflow_started: false,
      next_cursor: null,
      recipients: [{ agent_id: delivery.agent_id, binding_id: `ibnd_${suffix}`, input_keys: [] }],
    })
    await processDiscordEvent(
      receipt({ channel_id: thread.id, content: 'continue', mentions: [] }),
      f.context,
    )
    expect(f.core.deliverInput.mock.calls[0]?.[1]).toMatchObject({
      binding_id: `ibnd_${suffix}`,
      input_key: `discord:message:${config.guildID}:${thread.id}:${event.id}`,
    })
    expect(f.core.listRoutes).not.toHaveBeenCalled()
    expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
    expect(f.calls.some((call) => call.startsWith('POST'))).toBe(false)
  })

  it.each([true, false])(
    'preserves receive revocation even when mentioned=%s',
    async (mentioned) => {
      const f = await fixture()
      f.existingThread()
      f.core.lookupRecipients.mockResolvedValue({
        channel_id: delivery.channel_id,
        has_receive_binding_history: true,
        workflow_started: false,
        recipients: [],
        next_cursor: null,
      })
      await processDiscordEvent(
        receipt({ channel_id: thread.id, mentions: mentioned ? event.mentions : [] }),
        f.context,
      )
      expect(f.core.deliverInput).not.toHaveBeenCalled()
      expect(f.core.listRoutes).not.toHaveBeenCalled()
      expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
    },
  )

  it('does not mistake a send-only existing address for receive history', async () => {
    const f = await fixture()
    f.core.lookupRecipients.mockResolvedValue({
      channel_id: delivery.channel_id,
      has_receive_binding_history: false,
      workflow_started: false,
      recipients: [],
      next_cursor: null,
    })
    await processDiscordEvent(receipt(), f.context)
    expect(f.core.deliverWorkflow).toHaveBeenCalledOnce()
    expect(f.core.deliverInput).not.toHaveBeenCalled()
    expect(f.core.deliverWorkflow.mock.calls[0]?.[1].target.parent).toBeUndefined()
  })

  it('reselects a newly bound recipient after atomic only-if-unbound rejection', async () => {
    const f = await fixture()
    f.core.deliverWorkflow.mockRejectedValueOnce(new ReceiptClientError('http_error', 409))
    f.core.lookupRecipients
      .mockResolvedValueOnce({
        has_receive_binding_history: false,
        workflow_started: false,
        recipients: [],
        next_cursor: null,
      })
      .mockResolvedValue({
        channel_id: delivery.channel_id,
        has_receive_binding_history: true,
        workflow_started: false,
        recipients: [{ agent_id: delivery.agent_id, binding_id: `ibnd_${suffix}`, input_keys: [] }],
        next_cursor: null,
      })
    await processDiscordEvent(receipt(), f.context)
    expect(f.core.deliverWorkflow).toHaveBeenCalledOnce()
    expect(f.core.deliverInput).toHaveBeenCalledOnce()
    expect(f.calls.filter((call) => call.startsWith('POST'))).toHaveLength(1)
  })

  it('does not recreate a thread or download files on semantic replay', async () => {
    const f = await fixture()
    f.core.lookupWorkflow.mockResolvedValue({
      exists: true,
      agent_state: 'active',
      input_keys: [inputKey],
    })
    await processDiscordEvent(receipt(), f.context)
    expect(f.calls.some((call) => call.startsWith('POST'))).toBe(false)
    expect(f.core.deliverWorkflow).toHaveBeenCalledOnce()
  })

  it('recovers a lost thread-create response by reading its deterministic address', async () => {
    let created = false
    const f = await fixture((request, response) => {
      if (request.method === 'POST') {
        created = true
        json(response, {}, 500)
        return true
      }
      if (created && request.url === `/channels/${thread.id}`) {
        json(response, thread)
        return true
      }
      return false
    })
    await processDiscordEvent(receipt(), f.context)
    expect(f.core.deliverWorkflow).toHaveBeenCalledOnce()
    expect(f.calls.filter((call) => call.startsWith('POST'))).toHaveLength(1)
  })

  it.each(['foreign', 'nonempty'])(
    'rejects all route configuration before partial delivery: %s',
    async (kind) => {
      const f = await fixture()
      f.core.listRoutes.mockResolvedValue([
        { id: `iroute_${suffix}`, behavior_key: 'discord_conversation', configuration: {} },
        {
          id: 'iroute_baaaaaaaaaaaaaaaaaaaaaaaae',
          behavior_key: kind === 'foreign' ? 'other' : 'discord_conversation',
          configuration: kind === 'nonempty' ? { surprise: true } : {},
        },
      ])
      await expect(processDiscordEvent(receipt(), f.context)).rejects.toEqual(
        new ReceiptBehaviorError(false),
      )
      expect(f.core.lookupWorkflow).not.toHaveBeenCalled()
      expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
      expect(f.calls.some((call) => call.startsWith('POST'))).toBe(false)
      expect(f.budget.usedBytes).toBe(0)
    },
  )

  it('stops unchanged conflict presentations and definite admission denial', async () => {
    const f = await fixture()
    f.core.deliverWorkflow.mockRejectedValue(new ReceiptClientError('http_error', 409))
    await expect(processDiscordEvent(receipt(), f.context)).rejects.toEqual(
      new ReceiptBehaviorError(true),
    )
    expect(f.core.deliverWorkflow).toHaveBeenCalledOnce()
    f.core.deliverWorkflow
      .mockClear()
      .mockRejectedValue(new ReceiptClientError('http_error', 409, 'managed_work_admission_denied'))
    await expect(processDiscordEvent(receipt(), f.context)).rejects.toEqual(
      new ReceiptBehaviorError(false),
    )
    expect(f.core.deliverWorkflow).toHaveBeenCalledOnce()
  })

  it('rejects empty ordinary content rather than treating successful mentions as proof of intent', async () => {
    const f = await fixture()
    f.existingThread()
    await expect(
      processDiscordEvent(receipt({ channel_id: thread.id, content: '', mentions: [] }), f.context),
    ).rejects.toEqual(new ReceiptBehaviorError(false))
    expect(f.core.lookupRecipients).not.toHaveBeenCalled()
  })

  it.each(['bot', 'self', 'unmentioned_root', 'unmapped_thread'])(
    'ignores %s without launching',
    async (kind) => {
      const f = await fixture()
      f.existingThread()
      const queued = receipt(
        kind === 'bot'
          ? { author: { ...event.author, bot: true } }
          : kind === 'self'
            ? { author: { ...event.author, id: config.botUserID } }
            : { mentions: [], channel_id: kind === 'unmapped_thread' ? thread.id : room.id },
      )
      await processDiscordEvent(queued, f.context)
      expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
      expect(f.core.deliverInput).not.toHaveBeenCalled()
      expect(f.calls).toEqual([])
      if (kind === 'unmentioned_root' || kind === 'unmapped_thread') {
        expect(f.core.lookupRecipients).toHaveBeenCalledOnce()
        expect(f.core.lookupRecipients.mock.calls[0]?.[1].provider_ref).toBe(
          kind === 'unmapped_thread' ? thread.id : room.id,
        )
        expect(f.core.listRoutes).not.toHaveBeenCalled()
      }
    },
  )
})
