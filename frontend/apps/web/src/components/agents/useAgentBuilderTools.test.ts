import { describe, expect, it } from 'vitest'
import { parse } from 'yaml'
import { z } from 'zod'

import { createBasicConfigSession, emptyBasicConfig } from '@/components/agents/useAgentBuilderForm'
import {
  agentBuilderToolsDefinition,
  agentBuilderToolsRequest,
} from '@/components/agents/useAgentBuilderTools'

function previewSource(request: ReturnType<typeof agentBuilderToolsRequest>) {
  const text = z.string().safeParse(request.source)
  return z.json().parse(text.success ? parse(text.data) : request.source)
}

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

it('includes preserved custom tools and opaque sources in tool resolution', () => {
  const session = createBasicConfigSession(`
instruction: Help.
model: {provider_config: openai, name: primary}
tools:
  custom_example:
    type: custom
    description: Example
    deferred: true
    input_schema: {type: object}
  web_search: {permission: {mode: always_ask}, extension: true}
machine_sources:
  - machine_pool_name: shared-pool
    extension: {value: retained}
mcp:
  docs: {url: 'https://example.com', extension: true, auth: {type: bearer, secret: example}}
`)
  if (!session.initialDraft) throw new Error('Missing draft')
  const request = agentBuilderToolsRequest(session.initialDraft)
  const preview = previewSource(request)
  expect(preview).toHaveProperty('tools.custom_example.deferred', true)
  expect(preview).toHaveProperty('tools.custom_example.input_schema.type', 'object')
  expect(preview).toHaveProperty('tools.web_search.extension', true)
  expect(preview).toHaveProperty('machine_sources.0.machine_pool_name', 'shared-pool')
  expect(preview).toHaveProperty('machine_sources.0.extension.value', 'retained')
  expect(preview).toHaveProperty('mcp.docs.extension', true)
  expect(preview).not.toHaveProperty('mcp.docs.auth')
})

it('keeps the last definition in tool previews while raw YAML contains an unresolved alias', () => {
  const source =
    'instruction: Help.\nmodel: {provider_config: openai, name: primary}\ntools: {example: {type: custom, description: Example, input_schema: {type: object}}}\n'
  const draft = createBasicConfigSession(source).initialDraft
  if (!draft) throw new Error('Missing draft')
  const request = agentBuilderToolsRequest({
    ...draft,
    advancedEntries: draft.advancedEntries?.map((entry) => ({ ...entry, source: '*missing' })),
  })
  expect(previewSource(request)).toHaveProperty('tools.example.description', 'Example')
})
