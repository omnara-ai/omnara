import type { IncomingMessage, ServerResponse } from 'node:http'

import { type ChannelConnectorEventReceipt, schemas } from '@omnara/sdk'
import { vi } from 'vitest'

import { WorkByteBudget } from '../work-budget'
import type { DiscordBehaviorContext } from './behavior'
import { type DiscordInboundMessage, discordInputKey } from './events'
import {
  app,
  config,
  identity,
  installation,
  json,
  message,
  room,
  server,
  suffix,
  thread,
} from './test-support'

export const event = {
  ...message,
  author: { id: '777777777777777777', username: 'Ada', global_name: 'Ada Lovelace', bot: false },
  content: `<@${config.botUserID}> help **exactly**`,
  mentions: [{ id: config.botUserID }],
}
export const inputKey = discordInputKey(event)
export const delivery = {
  agent_id: `agt_${suffix}`,
  channel_id: `itgt_${suffix}`,
  agent_input_id: `ain_${suffix}`,
  created_agent: true,
  created_input: true,
  content_blocks: [],
}
export function receipt(extra: Partial<DiscordInboundMessage> = {}): ChannelConnectorEventReceipt {
  return {
    receipt_id: `irec_${suffix}`,
    integration_app_id: app.app.id,
    integration_install_id: installation.install.id,
    event_id: 'discord:test-session:2',
    state: 'processing',
    lease_token: '01994550-1234-7123-8123-123456789abc',
    lease_generation: 1,
    lease_expires_at: new Date(Date.now() + 10_000).toISOString(),
    attempt_count: 1,
    last_error: {},
    payload: { op: 0, t: 'MESSAGE_CREATE', s: 2, d: { ...event, ...extra } },
  }
}

export async function fixture(
  provider?: (request: IncomingMessage, response: ServerResponse) => boolean,
) {
  const calls: string[] = []
  let threadExists = false
  const apiUrl = await server((request, response) => {
    calls.push(`${request.method} ${request.url}`)
    if (provider?.(request, response)) return
    if (identity(request, response)) return
    if (
      request.method === 'POST' &&
      request.url === `/channels/${room.id}/messages/${event.id}/threads`
    ) {
      threadExists = true
      json(response, thread)
    } else if (request.url === `/channels/${room.id}`) json(response, room)
    else if (request.url === `/channels/${thread.id}`)
      json(response, threadExists ? thread : {}, threadExists ? 200 : 404)
    else json(response, {}, 404)
  })
  const budget = new WorkByteBudget(256 * 1024 * 1024)
  const core = {
    getAppConfiguration: vi
      .fn<DiscordBehaviorContext['core']['getAppConfiguration']>()
      .mockResolvedValue(app),
    getInstallationConfiguration: vi
      .fn<DiscordBehaviorContext['core']['getInstallationConfiguration']>()
      .mockResolvedValue(installation),
    listRoutes: vi
      .fn<DiscordBehaviorContext['core']['listRoutes']>()
      .mockResolvedValue([
        { id: `iroute_${suffix}`, behavior_key: 'discord_conversation', configuration: {} },
      ]),
    publishDefinition: vi
      .fn<DiscordBehaviorContext['core']['publishDefinition']>()
      .mockImplementation((_scope, body) => Promise.resolve({ ...body, id: `cdef_${suffix}` })),
    lookupRecipients: vi
      .fn<DiscordBehaviorContext['core']['lookupRecipients']>()
      .mockResolvedValue({
        has_receive_binding_history: false,
        workflow_started: false,
        recipients: [],
        next_cursor: null,
      }),
    lookupWorkflow: vi
      .fn<DiscordBehaviorContext['core']['lookupWorkflow']>()
      .mockResolvedValue({ exists: false, input_keys: [] }),
    deliverInput: vi
      .fn<DiscordBehaviorContext['core']['deliverInput']>()
      .mockImplementation((queued, body) => {
        schemas.zDeliverChannelConnectorInputRequest.parse({
          ...body,
          receipt: {
            receipt_id: queued.receipt_id,
            lease_token: queued.lease_token,
            lease_generation: queued.lease_generation,
          },
        })
        return Promise.resolve({ ...delivery, created_agent: false })
      }),
    deliverWorkflow: vi
      .fn<DiscordBehaviorContext['core']['deliverWorkflow']>()
      .mockImplementation((queued, body) => {
        schemas.zDeliverChannelConnectorWorkflowRequest.parse({
          ...body,
          receipt: {
            receipt_id: queued.receipt_id,
            lease_token: queued.lease_token,
            lease_generation: queued.lease_generation,
          },
        })
        return Promise.resolve(delivery)
      }),
  }
  const context = {
    core,
    apiUrl,
    signal: new AbortController().signal,
    deadlineMs: Date.now() + 10_000,
    reserveWorkBytes: budget.reserve,
  } satisfies DiscordBehaviorContext
  return {
    context,
    core,
    calls,
    budget,
    existingThread: () => {
      threadExists = true
    },
  }
}
