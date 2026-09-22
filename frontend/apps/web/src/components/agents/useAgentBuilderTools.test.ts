import { describe, expect, it } from 'vitest'

import { createBasicConfigSession, emptyBasicConfig } from '@/components/agents/useAgentBuilderForm'
import { agentBuilderToolsSource } from '@/components/agents/useAgentBuilderTools'

describe('agentBuilderToolsSource', () => {
  it('forwards tool overrides and MCP loading settings without auth', () => {
    const payload: unknown = JSON.parse(
      agentBuilderToolsSource({
        ...emptyBasicConfig,
        tools: [
          { name: 'web_search', permission: null },
          { name: 'web_fetch', permission: { mode: 'always_ask', parameters: {} }, deferred: true },
          { name: 'ask_question', enabled: false, permission: null },
        ],
        mcpServers: [
          {
            id: 'server-1',
            name: 'docs',
            url: 'https://mcp.example.com',
            permission: null,
            defaultEnabled: false,
            deferred: true,
            authType: 'bearer',
            secretId: 'sec_123',
            service: '',
            region: '',
            tools: [
              { name: 'lookup', enabled: true, permission: null, deferred: false },
              { name: 'ignored', enabled: null, permission: null },
            ],
          },
        ],
      }),
    )
    expect(payload).toEqual({
      interaction_handlers: {},
      tools: {
        web_search: { type: 'built_in' },
        web_fetch: { type: 'built_in', permission: { mode: 'always_ask' }, deferred: true },
        ask_question: { type: 'built_in', enabled: false },
      },
      mcp: {
        docs: {
          url: 'https://mcp.example.com',
          default_enabled: false,
          deferred: true,
          tools: { lookup: { enabled: true, deferred: false } },
        },
      },
      machine_sources: [],
      skills: [],
      subagents: {},
    })
  })

  it('preserves independent capabilities and tool permissions through builder edits', () => {
    const tool = { permission: { mode: 'always_ask' }, deferred: true }
    const handler = {}
    const source = {
      instruction: 'Review',
      tools: { app__chat__post_message: tool },
      interaction_handlers: { chat: handler },
      event_webhook: {
        url: 'https://example.com/events',
        events: ['tool_call_update'],
        signing_secret_id: 'sec_example',
      },
    }
    const session = createBasicConfigSession(JSON.stringify(source))
    if (session.initialDraft === null) throw new Error('App source must support builder preview')
    const changed = { ...session.initialDraft, instruction: 'Updated instruction' }
    const preview: unknown = JSON.parse(agentBuilderToolsSource(changed))
    expect(preview).toMatchObject({
      tools: { app__chat__post_message: tool },
      interaction_handlers: source.interaction_handlers,
    })
    const updated = createBasicConfigSession(session.apply(changed)).initialDraft
    expect(updated?.interactionHandlers).toEqual(source.interaction_handlers)
    expect(updated?.eventWebhookUrl).toBe(source.event_webhook.url)
    expect(updated?.eventWebhookEvents).toEqual(source.event_webhook.events)
    expect(updated?.eventWebhookSigningSecretId).toBe(source.event_webhook.signing_secret_id)
    expect(updated?.tools[0]).toEqual({
      name: 'app__chat__post_message',
      permission: { mode: 'always_ask', parameters: {} },
      deferred: true,
    })
    const webhookEdit = {
      ...changed,
      eventWebhookUrl: 'https://example.com/updated',
      eventWebhookEvents: ['model_output', 'tool_call_update'],
    }
    const restored = createBasicConfigSession(
      createBasicConfigSession('').apply(webhookEdit),
    ).initialDraft
    expect(restored?.interactionHandlers).toEqual(changed.interactionHandlers)
    expect(restored?.tools).toEqual(changed.tools)
    expect(restored?.eventWebhookUrl).toBe(webhookEdit.eventWebhookUrl)
    expect(restored?.eventWebhookEvents).toEqual(webhookEdit.eventWebhookEvents)
  })
})
