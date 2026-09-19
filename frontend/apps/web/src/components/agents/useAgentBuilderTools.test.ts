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
      app_resources: {},
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

  it('preserves scoped app instances and selected tools through builder edits and preview', () => {
    const resource = {
      app_instance: `app_${'a'.repeat(26)}`,
      scope: { slack: { channel_id: 'C123', thread_ts: '123.456' } },
      tools: { slack_post_message: {} },
    }
    const session = createBasicConfigSession(
      JSON.stringify({ instruction: 'Review', app_resources: { chat: resource } }),
    )
    if (session.initialDraft === null) throw new Error('App source must support builder preview')
    const changed = { ...session.initialDraft, instruction: 'Updated instruction' }
    expect(JSON.parse(agentBuilderToolsSource(changed))).toHaveProperty(
      'app_resources.chat',
      resource,
    )
    expect(createBasicConfigSession(session.apply(changed)).initialDraft?.appResources).toEqual({
      chat: resource,
    })
  })
})
