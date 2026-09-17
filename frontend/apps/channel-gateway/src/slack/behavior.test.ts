import {
  type ChannelConnectorEventReceipt,
  type LookupChannelConnectorWorkflowResponse,
} from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import type { ReceiptWorkflowRequest } from '../core/client'
import { ReceiptClientError } from '../core/receipt-http'
import { processSlackEvent, type SlackBehaviorContext, slackDefinition } from './behavior'
import {
  filesKey,
  fixture,
  id,
  installation,
  plainKey,
  receipt,
  result,
} from './behavior-test-support'
import { body, credentials, deferred, json } from './test-support'

function active(keys: string[] = []): LookupChannelConnectorWorkflowResponse {
  return { exists: true, agent_state: 'active', input_keys: keys }
}
function delivered(
  context: Awaited<ReturnType<typeof fixture>>['context'],
  index = 0,
): ReceiptWorkflowRequest {
  const request = context.deliverWorkflow.mock.calls[index]?.[1]
  if (!request) throw new Error('missing delivery')
  return request
}

describe('verified Slack receipt behavior', () => {
  it('launches/reuses mentions with native keys, hidden/visible history and explicit configured grants', async () => {
    const { context, calls } = await fixture()
    const queued = receipt()
    const before = JSON.stringify(queued)
    await processSlackEvent(queued, context)
    expect(JSON.stringify(queued)).toBe(before)
    expect(context.lookupWorkflow).toHaveBeenCalledWith(
      queued,
      {
        route_id: `iroute_${id}`,
        instance_key: 'C1:100.000002',
        input_keys: [plainKey, filesKey],
      },
      expect.any(AbortSignal),
    )
    const request = delivered(context)
    expect(request).toMatchObject({
      instance_key: 'C1:100.000002',
      input_key: plainKey,
      input_precondition: { input_key: filesKey, exists: false },
      target: {
        definition_id: `cdef_${id}`,
        provider_ref: 'C1:100.000002',
        provider_ref_kind: 'thread',
        display_name: 'general',
      },
      grants: { read: false, send: true },
      author: { ref: 'U1', display_name: 'Alice' },
      delivery_mode: 'steering',
      cancel_open_interactions: true,
    })
    expect(request).not.toHaveProperty('receipt')
    expect(request.content_blocks).toEqual([
      {
        type: 'text',
        text: 'The agent was mentioned in a Slack channel, so this message starts a new Slack thread for communicating with the agent.\n\nRecent Slack context:\n<@U2> (Alice): previous <@UBOT> (Omnara)\n\n<@U1> (Alice) in <#C1> (#general), thread 100.000002:\n',
        metadata: { omnara_hidden: 'true' },
      },
      {
        type: 'text',
        text: '<@UBOT> (Omnara) use <#C2> (#general) & help',
        metadata: { omnara_display_text: '@Omnara use #general & help' },
      },
    ])
    expect(calls).toContain('/conversations.history')
    context.lookupWorkflow.mockResolvedValue(active())
    calls.length = 0
    await processSlackEvent(receipt({ thread_ts: '98.000001' }), context)
    expect(delivered(context, 1).instance_key).toBe('C1:98.000001')
    expect(delivered(context, 1).content_blocks[0]).toHaveProperty(
      'text',
      expect.stringContaining('already attached to this agent'),
    )
    expect(calls).not.toContain('/conversations.replies')
    expect(context.publishDefinition.mock.calls[0]?.[1]).toEqual(slackDefinition('thread'))
  })

  it('keeps a DM workflow persistent and appends unmentioned replies only to existing active threads', async () => {
    const { context, calls } = await fixture()
    await processSlackEvent(
      receipt({ type: 'message', channel: 'D1', channel_type: 'im' }),
      context,
    )
    expect(delivered(context).instance_key).toBe('D1')
    expect(delivered(context).content_blocks[0]).toMatchObject({
      text: '<@U1> (Alice) in Slack DM:\n',
    })
    expect(calls).not.toContain('/conversations.history')
    context.lookupWorkflow.mockResolvedValue(active())
    await processSlackEvent(
      receipt({ type: 'message', text: 'for another participant', thread_ts: '90.000001' }),
      context,
    )
    expect(delivered(context, 1).content_blocks[0]).toHaveProperty(
      'text',
      expect.stringContaining('whether to call `send_channel_message` at all'),
    )
  })

  it('keeps threaded IM input on the persistent DM and publishes no continuation capability', async () => {
    const { context } = await fixture()
    context.lookupWorkflow.mockResolvedValue(active())
    await processSlackEvent(
      receipt({ type: 'message', channel: 'D1', channel_type: 'im', thread_ts: '90.000001' }),
      context,
    )
    expect(delivered(context)).toMatchObject({
      instance_key: 'D1',
      target: {
        provider_ref: 'D1',
        provider_ref_kind: 'dm',
        display_name: 'Slack DM with Alice',
      },
    })
    expect(context.publishDefinition.mock.calls[0]?.[1]).toMatchObject({
      implementation_key: 'slack_dm',
      kind: 'SLACK_CHANNEL',
      capabilities: { creates_reply_channel: false, read: true, send: true },
    })
  })

  it('preserves the stored channel name when optional label lookup fails', async () => {
    const { context } = await fixture((request, response) => {
      if (request.url !== '/conversations.info') return false
      json(response, { ok: false, error: 'channel_not_found' })
      return true
    })
    await processSlackEvent(receipt(), context)
    expect(delivered(context).target.display_name).toBeUndefined()
  })

  it.each([
    { type: 'message', text: 'unmapped channel root' },
    { type: 'message', text: '<@UBOT> duplicate ordinary mention', thread_ts: '90.000001' },
    { user: 'UBOT' },
    { user: 'USLACKBOT' },
    { user: '' },
    { bot_id: 'B1' },
    { team: 'TREMOTE' },
    { source_team: 'TREMOTE' },
    { user_team: 'TREMOTE' },
    { subtype: 'message_changed' },
    { subtype: 'bot_message' },
    { type: 'reaction_added' },
  ])('ignores native unsupported or nonhuman/remote input %j', async (extra) => {
    const { context, calls } = await fixture()
    await processSlackEvent(receipt(extra), context)
    expect(context.lookupWorkflow).not.toHaveBeenCalled()
    expect(context.deliverWorkflow).not.toHaveBeenCalled()
    expect(calls).toEqual([])
  })

  it.each([
    {
      lookup: { exists: false, input_keys: [] },
      extra: { type: 'message', text: 'file', subtype: 'file_share' },
    },
    { lookup: { exists: true, agent_state: 'archived', input_keys: [] }, extra: {} },
  ] satisfies {
    lookup: LookupChannelConnectorWorkflowResponse
    extra: ChannelConnectorEventReceipt['payload']
  }[])(
    'ignores absent append-only or archived workflows before media',
    async ({ lookup, extra }) => {
      const { context, calls } = await fixture()
      context.lookupWorkflow.mockResolvedValue(lookup)
      await processSlackEvent(
        receipt({ ...extra, files: [{ id: 'F1', file_access: 'check_file_info' }] }),
        context,
      )
      expect(context.publishDefinition).not.toHaveBeenCalled()
      expect(context.deliverWorkflow).not.toHaveBeenCalled()
      expect(calls).toEqual([])
    },
  )

  it.each([filesKey, plainKey])(
    'maps accepted callback replay without downloading: %s',
    async (key) => {
      const { context, calls } = await fixture()
      context.lookupWorkflow.mockResolvedValue(active([key]))
      const extra = key === filesKey ? { files: [{ id: 'F1' }] } : {}
      await processSlackEvent(receipt(extra), context)
      expect(delivered(context).input_key).toBe(key)
      expect(delivered(context).input_precondition).toBeUndefined()
      expect(delivered(context).target.display_name).toBeUndefined()
      expect(calls).toEqual([])
    },
  )

  it('maps a plain callback to the accepted files sibling, with nonempty replay content', async () => {
    const { context, calls } = await fixture()
    context.lookupWorkflow.mockResolvedValue(active([filesKey]))
    await processSlackEvent(receipt(), context)
    expect(delivered(context).input_key).toBe(filesKey)
    expect(delivered(context).content_blocks.length).toBeGreaterThan(0)
    expect(delivered(context).input_precondition).toBeUndefined()
    expect(calls).toEqual([])
  })

  it.each([plainKey, filesKey])(
    'concurrent siblings preserve one visible message and safe receipt replay when %s wins',
    async (winner) => {
      const { context, calls } = await fixture((request, response) => {
        if (request.url !== '/file') return false
        response.end('file content')
        return true
      })
      const accepted = new Map<string, ReceiptWorkflowRequest>()
      const outcomes = new Map<string, string>()
      const initialLookups = deferred()
      const winnerAccepted = deferred()
      let lookups = 0
      context.lookupWorkflow.mockImplementation(async () => {
        const snapshot: LookupChannelConnectorWorkflowResponse = accepted.size
          ? active(Array.from(accepted.keys()))
          : { exists: false, input_keys: [] }
        lookups += 1
        if (lookups <= 2) {
          if (lookups === 2) initialLookups.resolve()
          await initialLookups.promise
        }
        return snapshot
      })
      const validate = context.deliverWorkflow.getMockImplementation()
      if (!validate) throw new Error('missing generated request validator')
      // Model the documented core response contract: replay first, then atomic
      // precondition and insertion. This is a behavior unit, not a storage test.
      context.deliverWorkflow.mockImplementation(async (queued, request, signal) => {
        await validate(queued, request, signal)
        if (request.input_key !== winner && accepted.size === 0) await winnerAccepted.promise
        const priorKey = outcomes.get(queued.receipt_id)
        if (priorKey || accepted.has(request.input_key)) {
          outcomes.set(queued.receipt_id, priorKey ?? request.input_key)
          return { ...result, created_agent: false, created_input: false }
        }
        const condition = request.input_precondition
        if (condition && accepted.has(condition.input_key) !== condition.exists)
          throw new ReceiptClientError('http_error', 409)
        accepted.set(request.input_key, request)
        outcomes.set(queued.receipt_id, request.input_key)
        winnerAccepted.resolve()
        return result
      })
      const plain = receipt()
      const files = receipt({
        type: 'message',
        subtype: 'file_share',
        files: [{ name: 'notes.txt', url_private: `${context.apiUrl}/file` }],
      })
      files.receipt_id = 'irec_aaaaaaaaaaaaaaaaaaaaaaaabi'
      files.event_id = 'EvFiles'
      files.payload.event_id = files.event_id
      await Promise.all([processSlackEvent(plain, context), processSlackEvent(files, context)])
      expect(accepted.size).toBe(winner === plainKey ? 2 : 1)
      const content = Array.from(accepted.values()).flatMap((request) => request.content_blocks)
      expect(content.filter((block) => block.type === 'media')).toHaveLength(1)
      expect(
        content.filter(
          (block) =>
            block.type === 'text' &&
            block.metadata?.omnara_hidden !== 'true' &&
            block.text.includes('help'),
        ),
      ).toHaveLength(1)
      expect(outcomes.get(plain.receipt_id)).toBe(winner)
      expect(outcomes.get(files.receipt_id)).toBe(filesKey)
      expect(context.deliverWorkflow).toHaveBeenCalledTimes(3)

      // New lease/receipt, same semantic message: no process-local state or earlier
      // in-memory file preparation is needed to avoid duplicate media/downloads.
      const replay = {
        ...files,
        receipt_id: 'irec_aaaaaaaaaaaaaaaaaaaaaaaacq',
        event_id: 'EvReplay',
        payload: { ...files.payload, event_id: 'EvReplay' },
      }
      const acceptedBeforeReplay = Array.from(accepted.values())
      await processSlackEvent(replay, context)
      expect(Array.from(accepted.values())).toEqual(acceptedBeforeReplay)
      expect(outcomes.get(replay.receipt_id)).toBe(filesKey)
      expect(calls.filter((path) => path === '/file')).toHaveLength(1)
      expect(context.deliverWorkflow).toHaveBeenCalledTimes(4)
    },
  )

  it('rerenders the full presentation on a sibling race and reuses one media preparation', async () => {
    let lookedUp = false
    const { context, calls, work } = await fixture((request, response) => {
      if (request.url !== '/file') return false
      expect(lookedUp).toBe(true)
      expect(request.headers.authorization).toBe(`Bearer ${credentials.botToken}`)
      response.end('file content')
      return true
    })
    const lookup = vi
      .fn<SlackBehaviorContext['lookupWorkflow']>()
      .mockResolvedValueOnce(active())
      .mockResolvedValueOnce(active([plainKey]))
    context.lookupWorkflow.mockImplementation(async (...args) => {
      lookedUp = true
      return lookup(...args)
    })
    context.deliverWorkflow.mockRejectedValueOnce(new ReceiptClientError('http_error', 409))
    await processSlackEvent(
      receipt({
        files: [
          { id: 'F1', name: 'notes.txt', url_private: `${context.apiUrl}/file` },
          { name: 'oversize.txt', size: 10 * 1024 * 1024 + 1 },
        ],
      }),
      context,
    )
    const first = delivered(context)
    const second = delivered(context, 1)
    expect(first.content_blocks[1]).toHaveProperty('text', expect.stringContaining('help'))
    expect(first.content_blocks[1]).toHaveProperty(
      'metadata.omnara_display_text',
      expect.any(String),
    )
    expect(second.content_blocks[1]).toEqual({
      type: 'text',
      text: 'Files for the previous Slack message.',
      metadata: { omnara_hidden: 'true' },
    })
    expect(second.content_blocks[2]).toEqual({
      type: 'text',
      text: '\nSlack files not included:\n- oversize.txt skipped: too large',
      metadata: {
        omnara_display_text: 'Slack files not included:\n- oversize.txt skipped: too large',
      },
    })
    expect(first.content_blocks[3]).toBe(second.content_blocks[3])
    expect(second.content_blocks[3]).toMatchObject({
      type: 'media',
      filename: 'notes.txt',
      data: Buffer.from('file content').toString('base64'),
    })
    expect(second.input_precondition).toEqual({ input_key: plainKey, exists: true })
    expect(calls.filter((call) => call === '/file')).toHaveLength(1)
    expect(work.release).toHaveBeenCalledOnce()
  })

  it('renders delayed files neutrally on the first attempt when plain was already accepted', async () => {
    const { context } = await fixture()
    context.lookupWorkflow.mockResolvedValue(active([plainKey]))
    await processSlackEvent(receipt({ files: [{ name: 'not-available' }] }), context)
    expect(context.deliverWorkflow).toHaveBeenCalledOnce()
    expect(delivered(context).content_blocks[1]).toMatchObject({
      text: 'Files for the previous Slack message.',
      metadata: { omnara_hidden: 'true' },
    })
  })

  it('rejects unchanged 409 retries and does not treat unrelated errors as races', async () => {
    const { context } = await fixture()
    const conflict = new ReceiptClientError('http_error', 409)
    context.deliverWorkflow.mockRejectedValue(conflict)
    await expect(processSlackEvent(receipt(), context)).rejects.toBe(conflict)
    expect(context.deliverWorkflow).toHaveBeenCalledOnce()
    expect(context.lookupWorkflow).toHaveBeenCalledTimes(2)
    context.deliverWorkflow.mockClear().mockRejectedValue(new ReceiptClientError('http_error', 503))
    await expect(processSlackEvent(receipt(), context)).rejects.toMatchObject({ status: 503 })
    expect(context.deliverWorkflow).toHaveBeenCalledOnce()
  })

  it('bounds conflicting changing responses to four attempts', async () => {
    const { context } = await fixture()
    context.lookupWorkflow
      .mockResolvedValueOnce({ exists: false, input_keys: [] })
      .mockResolvedValueOnce(active())
      .mockResolvedValueOnce(active([plainKey]))
      .mockResolvedValueOnce(active([filesKey, plainKey]))
    context.deliverWorkflow.mockRejectedValue(new ReceiptClientError('http_error', 409))
    await expect(
      processSlackEvent(receipt({ files: [{ name: 'missing' }] }), context),
    ).rejects.toMatchObject({ status: 409 })
    expect(context.lookupWorkflow).toHaveBeenCalledTimes(4)
    expect(context.deliverWorkflow).toHaveBeenCalledTimes(4)
  })

  it('leaves receipt completion to the caller and delivers each configured route with its own grants', async () => {
    const { context } = await fixture()
    context.listRoutes.mockResolvedValue([
      { route_id: `iroute_${id}`, grants: { read: true, send: false } },
      { route_id: `iroute_aaaaaaaaaaaaaaaaaaaaaaaabi`, grants: { read: false, send: true } },
    ])
    await processSlackEvent(receipt(), context)
    expect(delivered(context).grants).toEqual({ read: true, send: false })
    expect(delivered(context, 1).grants).toEqual({ read: false, send: true })
    expect(delivered(context, 1).route_id).toBe('iroute_aaaaaaaaaaaaaaaaaaaaaaaabi')
  })

  it('does not reuse missing history from a mapped route for a new workflow on another route', async () => {
    const { context, calls } = await fixture()
    context.listRoutes.mockResolvedValue([
      { route_id: `iroute_${id}`, grants: { read: true, send: true } },
      { route_id: 'iroute_aaaaaaaaaaaaaaaaaaaaaaaabi', grants: { read: true, send: true } },
    ])
    context.lookupWorkflow
      .mockResolvedValueOnce(active())
      .mockResolvedValueOnce({ exists: false, input_keys: [] })
    await processSlackEvent(receipt(), context)
    expect(delivered(context).metadata?.history_status).toBe('skipped')
    expect(delivered(context, 1).metadata?.history_status).toBe('fetched')
    expect(calls.filter((path) => path === '/conversations.history')).toHaveLength(1)
  })

  it('validates queued scope and identity and never uses credentials for another installation', async () => {
    const { context, calls } = await fixture()
    const queued = receipt()
    queued.event_id = 'different'
    await expect(processSlackEvent(queued, context)).rejects.toMatchObject({
      code: 'invalid_event',
    })
    expect(context.getInstallation).not.toHaveBeenCalled()
    queued.event_id = 'Ev1'
    queued.integration_install_id = 'another'
    await expect(processSlackEvent(queued, context)).rejects.toMatchObject({
      code: 'event_scope_mismatch',
    })
    expect(calls).toEqual([])
    queued.integration_install_id = installation.install.id
    queued.payload.authorizations = [{ team_id: 'T1', user_id: 'ANOTHERBOT', is_bot: true }]
    await processSlackEvent(queued, context)
    expect(context.lookupWorkflow).not.toHaveBeenCalled()
  })

  it('aborts actual media I/O on cancellation and never delivers a partial input', async () => {
    const started = deferred()
    const closed = deferred()
    const { context, work } = await fixture((request, response) => {
      if (request.url !== '/slow') return false
      response.writeHead(200, { 'content-type': 'text/plain' })
      response.write('partial')
      response.on('close', closed.resolve)
      started.resolve()
      return true
    })
    const controller = new AbortController()
    context.signal = controller.signal
    context.lookupWorkflow.mockResolvedValue(active())
    const processing = processSlackEvent(
      receipt({ files: [{ name: 'notes.txt', url_private: `${context.apiUrl}/slow` }] }),
      context,
    )
    const rejected = expect(processing).rejects.toBeDefined()
    await started.promise
    controller.abort()
    await rejected
    await closed.promise
    expect(context.deliverWorkflow).not.toHaveBeenCalled()
    expect(work.release).toHaveBeenCalledOnce()
  })

  it('uses the earlier receipt lease deadline and cancels injected core I/O', async () => {
    const { context } = await fixture()
    const queued = receipt()
    queued.lease_expires_at = new Date(Date.now() + 35).toISOString()
    context.getInstallation.mockImplementation(
      async (_queued, signal) =>
        new Promise((_resolve, reject) => {
          signal.addEventListener(
            'abort',
            () => {
              reject(new Error('lease expired'))
            },
            { once: true },
          )
        }),
    )
    await expect(processSlackEvent(queued, context)).rejects.toThrow('lease expired')
    expect(context.lookupWorkflow).not.toHaveBeenCalled()
    queued.lease_expires_at = new Date(Date.now() - 1).toISOString()
    await expect(processSlackEvent(queued, context)).rejects.toMatchObject({
      code: 'deadline_exceeded',
    })
  })

  it('fetches existing-thread context once using native replies parameters', async () => {
    const { context } = await fixture((request, response) => {
      if (request.url !== '/conversations.replies') return false
      void body(request).then((bytes) => {
        expect(JSON.parse(bytes.toString())).toEqual({
          channel: 'C1',
          latest: '100.000002',
          inclusive: false,
          limit: 15,
          ts: '90.000001',
        })
        json(response, { ok: true, messages: [] })
      })
      return true
    })
    await processSlackEvent(receipt({ thread_ts: '90.000001' }), context)
    expect(delivered(context).metadata?.history_status).toBe('empty')
  })
})
