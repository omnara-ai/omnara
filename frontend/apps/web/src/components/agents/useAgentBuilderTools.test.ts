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
      listeners: {},
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

  it('preserves independent capabilities and fixed tool settings through builder edits', () => {
    const tool = { config: { channel_id: 'C123', thread_ts: '123.456' } }
    const listener = { config: { conversations: [{ channel_id: 'C123', thread_ts: '123.456' }] } }
    const handler = { config: { channel_id: 'C456' } }
    const source = {
      instruction: 'Review',
      tools: { app__chat__post_message: tool },
      listeners: { chat__thread_messages: listener },
      interaction_handlers: { chat: handler },
    }
    const session = createBasicConfigSession(JSON.stringify(source))
    if (session.initialDraft === null) throw new Error('App source must support builder preview')
    const changed = { ...session.initialDraft, instruction: 'Updated instruction' }
    const preview: unknown = JSON.parse(agentBuilderToolsSource(changed))
    expect(preview).toMatchObject({
      tools: { app__chat__post_message: tool },
      listeners: source.listeners,
      interaction_handlers: source.interaction_handlers,
    })
    const updated = createBasicConfigSession(session.apply(changed)).initialDraft
    expect(updated?.listeners).toEqual(source.listeners)
    expect(updated?.interactionHandlers).toEqual(source.interaction_handlers)
    expect(updated?.tools[0]?.config).toEqual(tool.config)
  })
})
