import { zAgentConfigDefinition } from '@omnara/sdk/zod'
import { describe, expect, it } from 'vitest'
import { isNode, parse, parseDocument } from 'yaml'

import { configPath, customToolNameError } from './agentConfigCompatibility'
import fixture from './fixtures/inline-custom-tools.yaml?raw'
import { type BasicConfig, createBasicConfigSession, emptyBasicConfig } from './useAgentBuilderForm'

function sessionFor(source = fixture) {
  const session = createBasicConfigSession(source)
  const draft = session.initialDraft
  if (!draft) throw new Error(JSON.stringify(session.diagnostics))
  return { session, draft }
}

function nodeSource(source: string, path: string[]) {
  const node = parseDocument(source).getIn(path, true)
  if (!isNode(node) || !node.range) throw new Error(`Missing ${path.join('.')}`)
  return source.slice(node.range[0], node.range[1])
}

const original = zAgentConfigDefinition.parse(parse(fixture))
const customTools = Object.fromEntries(
  Object.entries(original.tools ?? {}).filter(([, tool]) => tool.type === 'custom'),
)

it.each(['', 'bad-name', 'mcp__reserved', 'int__reserved', 'call_deferred_tool', 'a'.repeat(65)])(
  'rejects invalid or reserved custom tool names: %s',
  (name) => {
    expect(customToolNameError(name)).toBeDefined()
  },
)

function expectCustomUnchanged(source: string, baseline = fixture) {
  for (const name of Object.keys(customTools)) {
    expect(nodeSource(source, ['tools', name])).toBe(nodeSource(baseline, ['tools', name]))
  }
}

describe('mixed managed and advanced configurations', () => {
  it('opens all eight inline tools in Builder with scoped diagnostics and an exact no-op', () => {
    const { session, draft } = sessionFor()
    expect(draft.tools).toHaveLength(3)
    expect(draft.advancedEntries).toHaveLength(8)
    expect(session.diagnostics).toEqual([])
    expect(session.apply(draft)).toBe(fixture)
  })

  it.each<[string, (draft: BasicConfig) => Partial<BasicConfig>, string, unknown]>([
    [
      'instruction',
      () => ({ instruction: 'Updated instructions.\nSecond line.' }),
      'instruction',
      'Updated instructions.\nSecond line.',
    ],
    ['model', () => ({ modelName: 'another-model' }), 'model.name', 'another-model'],
    ['reasoning', () => ({ reasoningEffort: 'low' }), 'model.reasoning.effort', 'low'],
    [
      'permission',
      (draft) => ({
        tools: draft.tools.map((tool) => ({
          ...tool,
          permission: { mode: 'always_allow', parameters: {} },
        })),
      }),
      'tools.run_command.permission',
      { mode: 'always_allow' },
    ],
    [
      'machine source',
      (draft) => ({
        machineSources: draft.machineSources.map((row) => ({ ...row, name: 'another-pool' })),
      }),
      'machine_sources.0.machine_pool_name',
      'another-pool',
    ],
    [
      'adding tools',
      (draft) => ({ tools: [...draft.tools, { name: 'ask_question', permission: null }] }),
      'tools',
      { ...original.tools, ask_question: { type: 'built_in' } },
    ],
    ['removing every managed tool', () => ({ tools: [] }), 'tools', customTools],
  ])(
    'saves %s edits while preserving custom definitions byte-for-byte',
    (_label, edit, path, expected) => {
      const { session, draft } = sessionFor()
      const result = session.evaluate({ ...draft, ...edit(draft) })
      expect(result.issues).toEqual([])
      expect(parse(result.yaml)).toHaveProperty(path, expected)
      expectCustomUnchanged(result.yaml)
    },
  )

  it('rejects managed names owned by custom or opaque entries without corrupting source', () => {
    const { session, draft } = sessionFor(fixture + 'future: {a: b}\n')
    const collision = {
      ...draft,
      tools: [...draft.tools, { name: 'search_records', permission: null }],
    }
    const result = session.evaluate(collision)
    expect(result.issues).toMatchObject([{ path: ['tools', 'search_records'], error: true }])
    expect(result.yaml).toBe(fixture + 'future: {a: b}\n')
  })

  it('saves intentional description and arbitrary nested schema edits', () => {
    const { session, draft } = sessionFor()
    expect(
      draft.advancedEntries?.find((entry) => entry.path[1] === 'publish_report')?.source,
    ).toContain(
      'description: >-\n  Publish a report for the example project.\n  Include the selected records.',
    )
    const raw = `# Added explanation
type: custom
description: >-
  Updated public example
  description.
input_schema:
  type: object # schema comment
  properties:
    payload:
      anyOf:
        - {type: array, items: {type: object, additionalProperties: {type: string}}}
        - {$ref: '#/$defs/payload'}
  $defs: {payload: {type: object, properties: {id: {type: string}}}}
`
    const updated = session.apply({
      ...draft,
      advancedEntries: draft.advancedEntries?.map((entry) =>
        entry.path[1] === 'search_records' ? { ...entry, source: raw } : entry,
      ),
    })
    expect(parse(updated)).toHaveProperty(['tools', 'search_records'], parse(raw))
    expect(updated).toContain('# Added explanation')
    expect(updated).toContain('type: object # schema comment')
    expect(nodeSource(updated, ['tools', 'publish_report'])).toBe(
      nodeSource(fixture, ['tools', 'publish_report']),
    )
  })

  it.each([
    ['type: custom\ninput_schema: [', '/tools/search_records'],
    [
      'type: custom\ndescription: Example\ninput_schema: {type: array}',
      '/tools/search_records/input_schema/type',
    ],
    [
      'type: custom\ndescription: Example\ninput_schema: {type: object, required: [missing]}',
      '/tools/search_records/input_schema/required',
    ],
    ['type: custom\ninput_schema: {type: object}', '/tools/search_records/description'],
    [
      'type: custom\ndescription: Example\ninput_schema: {type: object, properties: {a: string}}',
      '/tools/search_records/input_schema/properties/a',
    ],
    [
      'type: custom\nname: misplaced\ndescription: Example\ninput_schema: {type: object}',
      '/tools/search_records',
    ],
  ])('blocks invalid custom definitions with an exact path: %s', (raw, path) => {
    const { session, draft } = sessionFor()
    const invalid = {
      ...draft,
      advancedEntries: draft.advancedEntries?.map((entry) =>
        entry.path[1] === 'search_records' ? { ...entry, source: raw } : entry,
      ),
    }
    const result = session.evaluate(invalid)
    expect(result.issues.map((issue) => configPath(issue.path))).toContain(path)
    expect(result.yaml).toBe(fixture)
  })

  it('supports JSON including adding and removing managed tools while preserving custom source', () => {
    const source = JSON.stringify(parse(fixture), null, 4)
    const { session, draft } = sessionFor(source)
    expect(session.apply(draft)).toBe(source)
    const updated = session.apply({
      ...draft,
      instruction: 'Changed.',
      tools: [{ name: 'ask_question', permission: null }],
    })
    expect(JSON.parse(updated)).toHaveProperty('instruction', 'Changed.')
    expect(JSON.parse(updated)).toHaveProperty('tools', {
      ...customTools,
      ask_question: { type: 'built_in' },
    })
    expectCustomUnchanged(updated, source)
  })

  it('cannot delete opaque tool siblings when applyNamedEntries removes all managed entries', () => {
    const source =
      fixture.replace(
        '  run_command:',
        '  future_tool: {enabled: false, future: {arbitrary: [1, "two"]}} # keep\n  run_command:',
      ) + '# keep extension\nextension: {quoted: "verbatim", list: [a,b,c]}\n'
    const { session, draft } = sessionFor(source)
    const updated = session.apply({ ...draft, tools: [], instruction: 'Changed.' })
    expect(updated).toContain(
      '  future_tool: {enabled: false, future: {arbitrary: [1, "two"]}} # keep',
    )
    expect(updated).toContain('# keep extension\nextension: {quoted: "verbatim", list: [a,b,c]}')
    expect(parse(updated)).not.toHaveProperty(['tools', 'run_command'])
    expect(parse(updated)).toHaveProperty(['tools', 'search_records'])
  })

  it.each(['', '            # required: [q]\n', '            # required: [q]\n    # next tool\n'])(
    'keeps raw definition indentation and comments stable across saves: %s',
    (comments) => {
      let source = `instruction: Help.
model: {provider_config: openai, name: primary}
tools:
    example:
        type: custom
        description: Example
        input_schema:
            type: object
            description: |+
                Preserve this trailing blank line.

${comments}    web_search: {}
`
      for (const description of ['Updated', 'Changed again']) {
        const { session, draft } = sessionFor(source)
        const result = session.evaluate({
          ...draft,
          advancedEntries: draft.advancedEntries?.map((entry) => ({
            ...entry,
            source: entry.source.replace(
              /description: (Example|Updated)/,
              `description: ${description}`,
            ),
          })),
        })
        const expected = source.replace(
          /description: (Example|Updated)/,
          `description: ${description}`,
        )
        expect(result.issues).toEqual([])
        expect(result.yaml).toBe(expected)
        source = result.yaml
      }
      const { session, draft } = sessionFor(source)
      const removed = session.evaluate({
        ...draft,
        advancedEntries: draft.advancedEntries?.map((entry) => ({ ...entry, removed: true })),
      })
      expect(removed.issues).toEqual([])
      expect(parse(removed.yaml)).toHaveProperty('tools', { web_search: {} })
      if (comments.includes('# next tool')) expect(removed.yaml).toContain('    # next tool\n')
    },
  )

  it('requires explicit removal intent and refuses to delete opaque definitions', () => {
    const source = fixture + 'extension: {future: true}\n'
    const { session, draft } = sessionFor(source)
    expect(session.apply({ ...draft, advancedEntries: [] })).toBe(source)
    const opaque = draft.advancedEntries?.find((entry) => entry.kind === 'opaque')
    if (!opaque) throw new Error('Missing opaque entry')
    for (const changes of [{ removed: true }, { source: '{future: false}' }]) {
      const result = session.evaluate({ ...draft, advancedEntries: [{ ...opaque, ...changes }] })
      expect(result.yaml).toBe(source)
      expect(result.issues).toMatchObject([{ path: ['extension'], error: true }])
    }
  })
})

it.each(['tools: null', 'tools:', 'tools: {}'])(
  'adds tools to a valid empty section: %s',
  (section) => {
    const source = `instruction: Help.\nmodel: {provider_config: openai, name: primary}\n${section}\n`
    const { session, draft } = sessionFor(source)
    const result = session.evaluate({ ...draft, tools: [{ name: 'web_search', permission: null }] })
    expect(result.issues).toEqual([])
    expect(parse(result.yaml)).toHaveProperty(['tools', 'web_search'])
  },
)

it.each([
  fixture,
  JSON.stringify(parse(fixture)),
  'tools: null # keep parent\n',
  'tools: {} # keep parent\n',
  'instruction: Help.\n',
  '',
])('inserts a custom definition without rewriting existing content (%#)', (source) => {
  const session = createBasicConfigSession(source)
  const draft = session.initialDraft ?? emptyBasicConfig
  const entry = {
    path: ['tools', 'new_tool'],
    kind: 'custom' as const,
    source:
      'type: custom\ndescription: New tool.\ninput_schema: {type: object, properties: {items: {type: array, items: {type: string}}}}\n',
  }
  const isJson = source.startsWith('{')
  if (isJson)
    entry.source = JSON.stringify(parse(entry.source)).replace(
      '"type":"string"',
      '"type":"integer","const":9007199254740993',
    )
  else
    entry.source =
      entry.source.replace('type: string', 'type: integer, const: 9007199254740993') +
      '# keep schema comment\n'
  const result = session.evaluate({
    ...draft,
    advancedEntries: [...(draft.advancedEntries ?? []), entry],
  })
  expect(result.issues).toEqual([])
  expect(parse(result.yaml)).toHaveProperty(['tools', 'new_tool'], parse(entry.source))
  expect(result.yaml).toContain('9007199254740993')
  if (!isJson) {
    expect(result.yaml).toContain('# keep schema comment')
  }
  if (source.includes('search_records')) expectCustomUnchanged(result.yaml, source)
  if (source.includes('# keep parent')) expect(result.yaml).toContain('# keep parent')
  const reopened = sessionFor(result.yaml)
  expect(reopened.draft.advancedEntries?.map((item) => item.path[1])).toContain('new_tool')
  expect(reopened.session.apply(reopened.draft)).toBe(result.yaml)
  expect(session.apply(draft)).toBe(source)
})

it('rejects custom insertions that collide with managed or advanced names', () => {
  const { session, draft } = sessionFor()
  const entry = {
    path: ['tools', 'new_tool'],
    kind: 'custom' as const,
    source: 'type: custom\ndescription: New.\ninput_schema: {type: object}',
  }
  for (const name of ['run_command', 'search_records']) {
    const result = session.evaluate({
      ...draft,
      advancedEntries: [...(draft.advancedEntries ?? []), { ...entry, path: ['tools', name] }],
    })
    expect(result.issues).toEqual(
      expect.arrayContaining([expect.objectContaining({ path: ['tools', name], error: true })]),
    )
    expect(result.yaml).toBe(fixture)
    expect(result.unappliedChanges).toBe(true)
  }
  const result = session.evaluate({
    ...draft,
    tools: [...draft.tools, { name: 'new_tool', permission: null }],
    advancedEntries: [...(draft.advancedEntries ?? []), entry],
  })
  expect(result.issues).toEqual(
    expect.arrayContaining([expect.objectContaining({ path: entry.path, error: true })]),
  )
  expect(result.yaml).toBe(fixture)
})

it('recovers JSON format from legacy YAML metadata and blocks cross-format tool edits', () => {
  const source = JSON.stringify(parse(fixture))
  const session = createBasicConfigSession(source, 'yaml')
  const draft = session.initialDraft
  if (!draft) throw new Error('Missing JSON draft')
  expect(session.sourceFormat).toBe('json')
  expect(session.apply(draft)).toBe(source)
  const result = session.evaluate({
    ...draft,
    advancedEntries: draft.advancedEntries?.map((entry) => ({
      ...entry,
      source: 'type: custom\ndescription: Example.\ninput_schema: {type: object}',
    })),
  })
  expect(result.issues[0]).toMatchObject({ path: ['tools', 'search_records'], error: true })
  expect(result.issues[0]?.message).toContain('Enter valid JSON')
  expect(result.unappliedChanges).toBe(true)
  expect(result.yaml).toBe(source)
})
