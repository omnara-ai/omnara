import { describe, expect, it } from 'vitest'
import { isNode, parse, parseDocument } from 'yaml'

import { configPath } from './agentConfigCompatibility'
import fixture from './fixtures/inline-custom-tools.yaml?raw'
import { type BasicConfig, createBasicConfigSession } from './useAgentBuilderForm'

function sessionFor(source = fixture) {
  const session = createBasicConfigSession(source)
  const draft = session.initialDraft
  if (!draft) throw new Error(JSON.stringify(session.diagnostics))
  return { session, draft }
}

function nodeSource(source: string, path: string[]) {
  const node = parseDocument(source).getIn(path, true)
  if (!isNode(node) || !node.range) throw new Error(`Missing ${path.join('.')}`)
  return source.slice(node.range[0], node.range[2])
}

function expectCustomUnchanged(source: string) {
  const { draft } = sessionFor()
  for (const entry of draft.advancedEntries ?? []) {
    expect(nodeSource(source, entry.path)).toBe(nodeSource(fixture, entry.path))
  }
}

describe('mixed managed and advanced configurations', () => {
  it('opens all eight inline tools in Builder with scoped diagnostics and an exact no-op', () => {
    const { session, draft } = sessionFor()
    expect(draft.tools).toHaveLength(3)
    expect(draft.advancedEntries).toHaveLength(8)
    expect(session.diagnostics.every((issue) => issue.kind === 'advanced' && !issue.error)).toBe(
      true,
    )
    expect(session.apply(draft)).toBe(fixture)
  })

  it.each<[string, (draft: BasicConfig) => BasicConfig]>([
    ['instruction', (draft) => ({ ...draft, instruction: 'Updated instructions.\nSecond line.' })],
    ['model', (draft) => ({ ...draft, modelName: 'another-model' })],
    ['reasoning', (draft) => ({ ...draft, reasoningEffort: 'low' })],
    [
      'permission',
      (draft) => ({
        ...draft,
        tools: draft.tools.map((tool) => ({
          ...tool,
          permission: { mode: 'always_allow', parameters: {} },
        })),
      }),
    ],
    [
      'machine source',
      (draft) => ({
        ...draft,
        machineSources: draft.machineSources.map((row) => ({ ...row, name: 'another-pool' })),
      }),
    ],
    [
      'adding tools',
      (draft) => ({
        ...draft,
        tools: [...draft.tools, { name: 'ask_question', permission: null }],
      }),
    ],
    ['removing every managed tool', (draft) => ({ ...draft, tools: [] })],
    [
      'renaming a managed tool',
      (draft) => ({ ...draft, tools: [{ name: 'ask_question', permission: null }] }),
    ],
  ])('preserves custom definitions byte-for-byte when editing %s', (_label, edit) => {
    const { session, draft } = sessionFor()
    const updated = session.apply(edit(draft))
    expect(updated).not.toBe(fixture)
    expect(parseDocument(updated).errors).toEqual([])
    expectCustomUnchanged(updated)
  })

  it('rejects managed names owned by custom or opaque entries without corrupting source', () => {
    const { session, draft } = sessionFor(fixture + 'future: {a: b}\n')
    const collision = {
      ...draft,
      tools: [...draft.tools, { name: 'search_records', permission: null }],
    }
    expect(session.validate(collision)).toMatchObject([
      { path: ['tools', 'search_records'], error: true },
    ])
    expect(session.apply(collision)).toBe(fixture + 'future: {a: b}\n')
  })

  it('saves intentional description and arbitrary nested schema edits', () => {
    const { session, draft } = sessionFor()
    const raw = `type: custom
description: Updated public example description.
input_schema:
  type: object
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
  ])('blocks invalid custom definitions with an exact path: %s', (raw, path) => {
    const { session, draft } = sessionFor()
    const invalid = {
      ...draft,
      advancedEntries: draft.advancedEntries?.map((entry) =>
        entry.path[1] === 'search_records' ? { ...entry, source: raw } : entry,
      ),
    }
    expect(session.validate(invalid).map((issue) => configPath(issue.path))).toContain(path)
    expect(session.apply(invalid)).toBe(fixture)
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
    expect(parse(updated)).toHaveProperty('instruction', 'Changed.')
    expect(nodeSource(updated, ['tools', 'search_records'])).toBe(
      nodeSource(source, ['tools', 'search_records']),
    )
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
    expect(
      session.validate({ ...draft, tools: [{ name: 'future_tool', permission: null }] }),
    ).toMatchObject([{ path: ['tools', 'future_tool'] }])
  })

  it.each([
    ['tools: &shared {}\n', 'anchors', ['tools']],
    ['tools: *shared\n', 'aliases', ['tools']],
    ['tools: {<<: {web_search: {}}}\n', 'merge keys', ['tools', '<<']],
  ])('preserves unsafe YAML and explains the exact construct', (yaml, reason, path) => {
    const session = createBasicConfigSession(yaml)
    expect(session.initialDraft).toBeNull()
    expect(
      session.diagnostics.some(
        (issue) =>
          issue.path.join('/') === path.join('/') &&
          issue.kind === 'fatal' &&
          issue.message.includes(reason),
      ),
    ).toBe(true)
    expect(session.apply(sessionFor().draft)).toBe(yaml)
  })
})

it('preserves comments around opaque siblings in a flow tools map', () => {
  const source = `instruction: Help.
model: {provider_config: openai, name: primary}
tools: {
  web_search: {}, # comment after the managed tool
  # comment before the opaque tool
  future: {unknown: [1,2,3]}, # comment after the opaque tool
  web_fetch: {}
}
`
  const { session, draft } = sessionFor(source)
  const result = session.evaluate({ ...draft, tools: [{ name: 'ask_question', permission: null }] })
  expect(result.issues).toEqual([])
  for (const comment of [
    'comment after the managed tool',
    'comment before the opaque tool',
    'comment after the opaque tool',
  ])
    expect(result.yaml).toContain(comment)
  expect(result.yaml).toContain('future: {unknown: [1,2,3]}')
  expect(parse(result.yaml)).toHaveProperty(['tools', 'ask_question'])
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

it('saves comments intentionally added in the raw definition editor', () => {
  const { session, draft } = sessionFor()
  const result = session.evaluate({
    ...draft,
    advancedEntries: draft.advancedEntries?.map((entry) =>
      entry.path[1] === 'search_records'
        ? {
            ...entry,
            source:
              '# Added explanation\n' +
              entry.source.replace('type: object', 'type: object # schema comment'),
          }
        : entry,
    ),
  })
  expect(result.issues).toEqual([])
  expect(result.yaml).toContain('# Added explanation')
  expect(result.yaml).toContain('type: object # schema comment')
  expect(nodeSource(result.yaml, ['tools', 'publish_report'])).toBe(
    nodeSource(fixture, ['tools', 'publish_report']),
  )
})
