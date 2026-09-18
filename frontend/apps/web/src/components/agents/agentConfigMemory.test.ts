import { describe, expect, it } from 'vitest'
import { parse } from 'yaml'

import { createBasicConfigSession } from '@/components/agents/useAgentBuilderForm'
import { agentBuilderToolsSource } from '@/components/agents/useAgentBuilderTools'

const source = `instruction: Help
model: {provider_config: provider, name: model}
memory_stores:
  - name: notes
    access: read_write
  - name: unavailable
    access: read_only
tools:
  write_file: {enabled: false}
  read_file: {permission: {mode: always_ask}}
`

describe('memory attachments in the builder', () => {
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
        { name: 'unavailable', access: 'read_only' },
        { name: 'new-store', access: 'read_write' },
      ],
      tools: { write_file: { enabled: false }, read_file: { permission: { mode: 'always_ask' } } },
    })
    const next = createBasicConfigSession(updated)
    expect(next.initialDraft && next.apply(next.initialDraft)).toBe(updated)
    expect(JSON.parse(agentBuilderToolsSource(draft))).toMatchObject({
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
