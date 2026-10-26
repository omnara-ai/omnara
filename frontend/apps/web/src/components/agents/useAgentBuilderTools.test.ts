import { describe, expect, it } from 'vitest'

import { createBasicConfigSession, emptyBasicConfig } from '@/components/agents/useAgentBuilderForm'
import { agentBuilderToolsDefinition } from '@/components/agents/useAgentBuilderTools'

describe('agentBuilderToolsDefinition', () => {
  it('forwards tool overrides and MCP loading settings without auth', () => {
    const payload = agentBuilderToolsDefinition({
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
    })
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
      memory_stores: [],
      subagents: {},
    })
  })

  it('preserves independent capabilities and tool permissions through builder edits', () => {
    const tool = { permission: { mode: 'always_ask' }, deferred: true }
    const handler = {}
    const source = {
      instruction: 'Review',
      model: { provider_config: 'anthropic', name: 'reviewer', reasoning: { effort: 'high' } },
      tools: { int__chat__post_message: tool },
      interaction_handlers: { chat: handler },
    }
    const session = createBasicConfigSession(JSON.stringify(source))
    if (session.initialDraft === null)
      throw new Error('Integration source must support builder preview')
    const changed = {
      ...session.initialDraft,
      instruction: 'Updated instruction',
      reasoningEffort: 'medium',
    }
    const preview = agentBuilderToolsDefinition(changed)
    expect(preview).toMatchObject({
      tools: { int__chat__post_message: tool },
      interaction_handlers: source.interaction_handlers,
    })
    const updated = createBasicConfigSession(session.apply(changed)).initialDraft
    expect(updated?.reasoningEffort).toBe('medium')
    expect(updated?.interactionHandlers).toEqual(source.interaction_handlers)
    expect(updated?.tools[0]).toEqual({
      name: 'int__chat__post_message',
      permission: { mode: 'always_ask', parameters: {} },
      deferred: true,
    })
    const restored = createBasicConfigSession(
      createBasicConfigSession('').apply(changed),
    ).initialDraft
    expect(restored?.interactionHandlers).toEqual(changed.interactionHandlers)
    expect(restored?.reasoningEffort).toBe('medium')
    expect(restored?.tools).toEqual(changed.tools)
  })
})
