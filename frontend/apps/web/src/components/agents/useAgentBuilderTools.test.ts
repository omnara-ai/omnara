import { describe, expect, it } from 'vitest'

import { emptyBasicConfig } from '@/components/agents/useAgentBuilderForm'
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
})
