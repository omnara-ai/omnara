import { describe, expect, it } from 'vitest'

import {
  zAgentInteractionDestination,
  zAppCapabilityDefinition,
  zCreateAgentRequest,
} from './generated/zod.gen'

describe('generated app capability contracts', () => {
  it('preserves tool policies, empty handler selections and subscriptions', () => {
    const source = {
      config: `acfg_${'a'.repeat(26)}`,
      tools: {
        app__support__post_message: {
          enabled: false,
          deferred: true,
          permission: { mode: 'always_ask' },
        },
      },
      interaction_handlers: { support: {} },
      subscriptions: [
        {
          app_id: `app_${'a'.repeat(26)}`,
          type: 'thread_messages',
          conversation: { channel_id: 'C123', thread_ts: '111.222' },
          events: ['message'],
        },
      ],
    }
    expect(zCreateAgentRequest.parse(source)).toEqual(source)
  })

  it('requires a static input schema in the catalog', () => {
    const capability = {
      description: 'Read Slack messages.',
      input_schema: {
        type: 'object',
        properties: { channel_id: { type: 'string' } },
      },
    }
    expect(zAppCapabilityDefinition.parse(capability)).toEqual(capability)
    expect(zAppCapabilityDefinition.safeParse({}).success).toBe(false)
  })

  it('preserves complete captured handler arguments', () => {
    const destination = {
      app_type: 'slack_thread',
      handler_key: 'support',
      app_id: `app_${'a'.repeat(26)}`,
      integration_target_id: `itgt_${'a'.repeat(26)}`,
      args: { channel_id: 'C123', thread_ts: '111.222' },
      address: { kind: 'thread', ref: 'C123:111.222' },
    }
    expect(zAgentInteractionDestination.parse(destination)).toEqual(destination)
  })
})
