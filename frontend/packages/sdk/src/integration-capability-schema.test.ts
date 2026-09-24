import { describe, expect, it } from 'vitest'

import {
  zAgentInteractionDestination,
  zCreateAgentRequest,
  zIntegrationCapabilityDefinition,
} from './generated/zod.gen'

describe('generated integration capability contracts', () => {
  it('preserves tool policies, empty handler selections and subscriptions', () => {
    const source = {
      config: `acfg_${'a'.repeat(26)}`,
      tools: {
        int__support__post_message: {
          enabled: false,
          deferred: true,
          permission: { mode: 'always_ask' },
        },
      },
      interaction_handlers: { support: {} },
      subscriptions: [
        {
          integration_id: `itg_${'a'.repeat(26)}`,
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
    expect(zIntegrationCapabilityDefinition.parse(capability)).toEqual(capability)
    expect(zIntegrationCapabilityDefinition.safeParse({}).success).toBe(false)
  })

  it('preserves complete captured handler arguments', () => {
    const destination = {
      integration_type: 'slack_thread',
      handler_key: 'support',
      integration_id: `itg_${'a'.repeat(26)}`,
      integration_target_id: `itgt_${'a'.repeat(26)}`,
      args: { channel_id: 'C123', thread_ts: '111.222' },
      address: { kind: 'thread', ref: 'C123:111.222' },
    }
    expect(zAgentInteractionDestination.parse(destination)).toEqual(destination)
  })
})
