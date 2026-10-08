import { describe, expect, it } from 'vitest'
import { parse } from 'yaml'

import { createBasicConfigSession } from '@/components/agents/useAgentBuilderForm'
import { agentBuilderToolsDefinition } from '@/components/agents/useAgentBuilderTools'

const source = `instruction: Help
model: {provider_config: provider, name: model}
memory_stores:
  - name: notes
    access: read_write
  - name: unavailable
    access: read
tools:
  write_file: {enabled: false}
  read_file: {permission: {mode: always_ask}}
`

describe('memory attachments in the builder', () => {
  it('preserves Git credentials and integration capabilities while editing memory attachments', () => {
    const combined =
      source +
      `
git_credentials: {integration: reviews}
interaction_handlers: {chat: {}}
`
    const session = createBasicConfigSession(combined)
    const draft = session.initialDraft
    if (!draft) throw new Error('Missing draft')
    const changed = {
      ...draft,
      memoryStores: [{ name: 'notes', access: 'read' as const }],
      tools: [
        ...draft.tools,
        {
          name: 'int__chat__post_message',
          permission: { mode: 'always_ask', parameters: {} },
          deferred: true,
        },
      ],
    }
    const updated = session.apply(changed)
    expect(parse(updated)).toMatchObject({
      git_credentials: { integration: 'reviews' },
      interaction_handlers: { chat: {} },
      memory_stores: [{ name: 'notes', access: 'read' }],
      tools: {
        write_file: { enabled: false },
        read_file: { permission: { mode: 'always_ask' } },
        int__chat__post_message: { permission: { mode: 'always_ask' }, deferred: true },
      },
    })
    expect(agentBuilderToolsDefinition(changed)).toMatchObject({
      interaction_handlers: { chat: {} },
      memory_stores: changed.memoryStores,
      tools: {
        write_file: { type: 'built_in', enabled: false },
        int__chat__post_message: { permission: { mode: 'always_ask' }, deferred: true },
      },
    })
    const next = createBasicConfigSession(updated)
    if (!next.initialDraft) throw new Error('Missing round-trip draft')
    expect(next.apply(next.initialDraft)).toBe(updated)
    expect(parse(next.apply({ ...next.initialDraft, interactionHandlers: {} }))).toMatchObject({
      git_credentials: { integration: 'reviews' },
      memory_stores: [{ name: 'notes', access: 'read' }],
    })
  })

  it('preserves existing attachments and tool overrides through Basic/YAML round trips', () => {
    const session = createBasicConfigSession(source)
    const draft = session.initialDraft
    expect(draft).not.toBeNull()
    if (!draft) throw new Error('Missing draft')
    expect(session.apply(draft)).toBe(source)
    const updated = session.apply({
      ...draft,
      instruction: 'Updated',
      memoryStores: [...draft.memoryStores, { name: 'new-store', access: 'read_write' }],
    })
    expect(parse(updated)).toMatchObject({
      memory_stores: [
        { name: 'notes', access: 'read_write' },
        { name: 'unavailable', access: 'read' },
        { name: 'new-store', access: 'read_write' },
      ],
      tools: { write_file: { enabled: false }, read_file: { permission: { mode: 'always_ask' } } },
    })
    const next = createBasicConfigSession(updated)
    expect(next.initialDraft && next.apply(next.initialDraft)).toBe(updated)
    expect(agentBuilderToolsDefinition(draft)).toMatchObject({
      memory_stores: draft.memoryStores,
    })
  })

  it('removes detached stores without changing tool overrides', () => {
    const session = createBasicConfigSession(source)
    const draft = session.initialDraft
    if (!draft) throw new Error('Missing draft')
    const updated: unknown = parse(session.apply({ ...draft, memoryStores: [] }))
    expect(updated).not.toHaveProperty('memory_stores')
    expect(updated).toMatchObject({
      tools: { write_file: { enabled: false }, read_file: { permission: { mode: 'always_ask' } } },
    })
  })
})
