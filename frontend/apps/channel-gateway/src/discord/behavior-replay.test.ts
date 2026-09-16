import { describe, expect, it } from 'vitest'

import { ReceiptClientError } from '../core/receipt-http'
import { ReceiptBehaviorError } from '../types'
import { processDiscordEvent } from './behavior'
import { delivery, fixture, inputKey, receipt } from './behavior-test-support'
import { config, installation, json, suffix, thread, user } from './test-support'

describe('Discord receipt replay and recipient authority', () => {
  it('finishes partial route fanout after restart without creating duplicate inputs or threads', async () => {
    const f = await fixture()
    const routes = [`iroute_${suffix}`, 'iroute_baaaaaaaaaaaaaaaaaaaaaaaae']
    const inputs = new Set<string>()
    let lostACK = true
    f.core.listRoutes.mockResolvedValue(
      routes.map((id) => ({ id, behavior_key: 'discord_conversation', configuration: {} })),
    )
    f.core.lookupRecipients.mockImplementation(() =>
      Promise.resolve({
        channel_id: inputs.size ? delivery.channel_id : undefined,
        has_receive_binding_history: inputs.size > 0,
        workflow_started: inputs.size > 0,
        recipients: [],
        next_cursor: null,
      }),
    )
    f.core.lookupWorkflow.mockImplementation((_receipt, query) =>
      Promise.resolve({
        exists: inputs.has(query.route_id),
        agent_state: inputs.has(query.route_id) ? 'active' : undefined,
        input_keys: inputs.has(query.route_id) ? [inputKey] : [],
      }),
    )
    f.core.deliverWorkflow.mockImplementation((_receipt, body) => {
      inputs.add(body.route_id)
      if (lostACK) {
        lostACK = false
        return Promise.reject(new ReceiptClientError('transport_failed'))
      }
      return Promise.resolve(delivery)
    })
    await expect(processDiscordEvent(receipt(), f.context)).rejects.toEqual(
      new ReceiptBehaviorError(true),
    )
    expect(inputs.size).toBe(1)
    // No process-local completion set survives this invocation. Core's saved
    // workflow/input facts decide replay and permit the remaining route.
    await processDiscordEvent(receipt(), { ...f.context, signal: new AbortController().signal })
    expect(inputs).toEqual(new Set(routes))
    expect(f.core.deliverWorkflow).toHaveBeenCalledTimes(3)
    expect(f.calls.filter((call) => call.startsWith('POST'))).toHaveLength(1)
  })

  it('paginates recipients, deduplicates repeated agents and never introduces workflow grants', async () => {
    const f = await fixture()
    f.existingThread()
    const first = { agent_id: delivery.agent_id, binding_id: `ibnd_${suffix}`, input_keys: [] }
    const second = {
      agent_id: 'agt_baaaaaaaaaaaaaaaaaaaaaaaae',
      binding_id: 'ibnd_baaaaaaaaaaaaaaaaaaaaaaaae',
      input_keys: [],
    }
    f.core.lookupRecipients
      .mockResolvedValueOnce({
        channel_id: delivery.channel_id,
        has_receive_binding_history: true,
        workflow_started: false,
        recipients: [first],
        next_cursor: 'page2',
      })
      .mockResolvedValue({
        channel_id: delivery.channel_id,
        has_receive_binding_history: true,
        workflow_started: false,
        recipients: [first, second],
        next_cursor: null,
      })
    await processDiscordEvent(receipt({ channel_id: thread.id, mentions: [] }), f.context)
    expect(f.core.deliverInput.mock.calls.map(([, body]) => body.binding_id)).toEqual([
      first.binding_id,
      second.binding_id,
    ])
    expect(f.core.lookupRecipients.mock.calls[1]?.[1].cursor).toBe('page2')
    expect(f.core.listRoutes).not.toHaveBeenCalled()
    expect(f.core.publishDefinition).not.toHaveBeenCalled()
  })

  it('rejects repeated cursors without an unbounded recipient loop', async () => {
    const f = await fixture()
    f.existingThread()
    f.core.lookupRecipients.mockResolvedValue({
      channel_id: delivery.channel_id,
      has_receive_binding_history: true,
      workflow_started: false,
      recipients: [],
      next_cursor: 'cycle',
    })
    await expect(
      processDiscordEvent(receipt({ channel_id: thread.id }), f.context),
    ).rejects.toEqual(new ReceiptBehaviorError(false))
    expect(f.core.lookupRecipients).toHaveBeenCalledTimes(2)
  })

  it('preserves a false receive-history observation during a separate-query recipient race', async () => {
    const f = await fixture()
    f.core.lookupRecipients.mockResolvedValue({
      channel_id: delivery.channel_id,
      has_receive_binding_history: false,
      workflow_started: false,
      next_cursor: null,
      recipients: [{ agent_id: delivery.agent_id, binding_id: `ibnd_${suffix}`, input_keys: [] }],
    })
    await processDiscordEvent(receipt(), f.context)
    expect(f.core.deliverInput).not.toHaveBeenCalled()
    expect(f.core.deliverWorkflow.mock.calls[0]?.[1].only_if_unbound).toBe(true)
  })

  it('ignores archived workflow agents without creating another native thread', async () => {
    const f = await fixture()
    f.core.lookupWorkflow.mockResolvedValue({
      exists: true,
      agent_state: 'archived',
      input_keys: [],
    })
    await processDiscordEvent(receipt(), f.context)
    expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
    expect(f.calls.some((call) => call.startsWith('POST'))).toBe(false)
  })

  it('rejects a mismatched installation before provider I/O', async () => {
    const f = await fixture()
    f.core.getInstallationConfiguration.mockResolvedValue({
      ...installation,
      install: { ...installation.install, id: 'iin_baaaaaaaaaaaaaaaaaaaaaaaae' },
    })
    await expect(processDiscordEvent(receipt(), f.context)).rejects.toEqual(
      new ReceiptBehaviorError(false),
    )
    expect(f.calls).toEqual([])
    expect(f.core.lookupRecipients).not.toHaveBeenCalled()
  })

  it.each(['application', 'bot'])(
    'revalidates %s identity on actionable unmentioned replies before admission',
    async (identity) => {
      const f = await fixture((request, response) => {
        if (identity === 'application' && request.url === '/applications/@me') {
          json(response, { id: config.guildID })
          return true
        }
        if (identity === 'bot' && request.url === '/users/@me') {
          json(response, { ...user, id: config.guildID })
          return true
        }
        return false
      })
      f.existingThread()
      f.core.lookupRecipients.mockResolvedValue({
        channel_id: delivery.channel_id,
        has_receive_binding_history: true,
        workflow_started: false,
        recipients: [{ agent_id: delivery.agent_id, binding_id: `ibnd_${suffix}`, input_keys: [] }],
        next_cursor: null,
      })
      await expect(
        processDiscordEvent(receipt({ channel_id: thread.id, mentions: [] }), f.context),
      ).rejects.toEqual(new ReceiptBehaviorError(false))
      expect(f.core.lookupRecipients).toHaveBeenCalledOnce()
      expect(f.core.deliverInput).not.toHaveBeenCalled()
      expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
      expect(f.calls).toEqual(['GET /applications/@me', 'GET /users/@me'])
    },
  )

  it('skips revoked receive history without provider reads or launching a replacement', async () => {
    const f = await fixture()
    f.core.lookupRecipients.mockResolvedValue({
      channel_id: delivery.channel_id,
      has_receive_binding_history: true,
      workflow_started: false,
      recipients: [],
      next_cursor: null,
    })
    await processDiscordEvent(receipt({ channel_id: thread.id, mentions: [] }), f.context)
    expect(f.calls).toEqual([])
    expect(f.core.deliverInput).not.toHaveBeenCalled()
    expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
    expect(f.core.listRoutes).not.toHaveBeenCalled()
  })
})
