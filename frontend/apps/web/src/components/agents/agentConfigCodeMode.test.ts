import { describe, expect, it } from 'vitest'
import { parse } from 'yaml'

import { type BasicConfig, createBasicConfigSession } from './useAgentBuilderForm'

const minimalYaml = `instruction: Do the thing.
model:
  provider_config: anthropic
  name: claude-sonnet-5
`

function mustDeserialize(source: string): BasicConfig {
  const config = createBasicConfigSession(source, 'yaml').initialDraft
  if (config == null) throw new Error('expected the config to deserialize')
  return config
}

function applyToSource(source: string, config: BasicConfig): string {
  return createBasicConfigSession(source, 'yaml').apply(config)
}

describe('code mode in the Builder form', () => {
  it('writes and clears code mode', () => {
    const draft = mustDeserialize(minimalYaml)
    expect(draft.codeMode).toBeUndefined()

    const withCodeMode = applyToSource(minimalYaml, { ...draft, codeMode: { enabled: true } })
    expect(parse(withCodeMode)).toMatchObject({ code_mode: { enabled: true } })
    expect(mustDeserialize(withCodeMode).codeMode).toEqual({ enabled: true })

    const cleared = applyToSource(withCodeMode, {
      ...mustDeserialize(withCodeMode),
      codeMode: undefined,
    })
    expect(parse(cleared)).toEqual(parse(minimalYaml))
  })

  it('preserves per-tool code_mode settings the form does not manage', () => {
    const source = `${minimalYaml}code_mode:
  enabled: true
tools:
  web_search:
    code_mode: false
mcp:
  search:
    url: https://mcp.example.com
    tools:
      lookup:
        code_mode: true
`
    const draft = mustDeserialize(source)
    expect(draft.codeMode).toEqual({ enabled: true })
    expect(draft.advancedEntries?.map((entry) => entry.path)).toEqual([
      ['tools', 'web_search'],
      ['mcp', 'search'],
    ])
    expect(parse(applyToSource(source, { ...draft, instruction: 'Do another thing.' }))).toEqual({
      ...parse(source),
      instruction: 'Do another thing.',
    })
  })

  it('keeps a custom tool code_mode setting editable without errors', () => {
    const source = `${minimalYaml}code_mode:
  enabled: true
  default_allowed: false
tools:
  create_ticket:
    type: custom
    description: Create a ticket.
    input_schema: {type: object}
    code_mode: true
`
    const session = createBasicConfigSession(source, 'yaml')
    expect(session.diagnostics.filter((issue) => issue.error)).toEqual([])
    const draft = mustDeserialize(source)
    expect(draft.advancedEntries).toEqual([
      expect.objectContaining({ path: ['tools', 'create_ticket'], kind: 'custom' }),
    ])
    const edited = applyToSource(source, {
      ...draft,
      advancedEntries: draft.advancedEntries?.map((entry) => ({
        ...entry,
        source: entry.source.replace('code_mode: true', 'code_mode: false'),
      })),
    })
    expect(parse(edited)).toEqual(parse(source.replace('code_mode: true', 'code_mode: false')))
  })
})
