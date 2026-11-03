import { describe, expect, it } from 'vitest'
import { isNode, parse, parseDocument } from 'yaml'

import { parseConfigDocument } from './agentConfigCompatibility'
import { editConfigDocument } from './agentConfigDocument'
import { createBasicConfigSession } from './agentConfigSession'
import { newMachineSource } from './useAgentBuilderForm'

const header = 'instruction: Help.\nmodel: {provider_config: example, name: primary}\n'
const sources = `${header}machine_sources:
  - machine_pool_name: managed-pool
    cwd: '/original' # managed comment
  - machine_pool_name: advanced-pool
    machine_provider_options_overlay: {future_option: 'unchanged'} # opaque comment
  - machine_name: managed-machine
`

function draftFor(source: string) {
  const session = createBasicConfigSession(source)
  if (!session.initialDraft) throw new Error(JSON.stringify(session.diagnostics))
  return { session, draft: session.initialDraft }
}

function nodeSource(source: string, path: (string | number)[]) {
  const node = parseDocument(source).getIn(path, true)
  if (!isNode(node) || !node.range) throw new Error('Missing node')
  return source.slice(node.range[0], node.range[2])
}

describe('document ownership', () => {
  it('rejects edits to a preserved path or its parent', () => {
    const source = `${header}tools: {managed: {}, opaque: {future: true}}\n`
    for (const path of [['tools'], ['tools', 'opaque'], ['tools', 'opaque', 'future']]) {
      const transaction = editConfigDocument(source, parseConfigDocument(source).doc, [
        ['tools', 'opaque'],
      ])
      expect(() => {
        transaction.set(path, {})
      }).toThrow('is preserved')
      expect(() => {
        transaction.remove(path)
      }).toThrow('is preserved')
      expect(transaction.serialize()).toBe(source)
    }
  })

  it('keeps supported machine rows editable beside an advanced row', () => {
    const { session, draft } = draftFor(sources)
    expect(draft.machineSources.map((row) => row.name)).toEqual(['managed-pool', 'managed-machine'])
    expect(session.diagnostics).toMatchObject([{ path: ['machine_sources', '1'], kind: 'opaque' }])
    const changed = {
      ...draft,
      machineSources: draft.machineSources.map((row) => ({ ...row, defaultCwd: '/updated' })),
    }
    const result = session.evaluate(changed)
    expect(result.issues).toEqual([])
    expect(parse(result.yaml)).toHaveProperty(['machine_sources', 0, 'cwd'], '/updated')
    expect(parse(result.yaml)).toHaveProperty(['machine_sources', 2, 'cwd'], '/updated')
    expect(nodeSource(result.yaml, ['machine_sources', 1])).toBe(
      nodeSource(sources, ['machine_sources', 1]),
    )
    expect(session.apply(draft)).toBe(sources)
    expect(session.apply(changed)).toBe(result.yaml)
  })

  it('removes and appends managed rows while retaining opaque row identity and source', () => {
    const { session, draft } = draftFor(sources)
    const result = session.evaluate({
      ...draft,
      machineSources: [{ ...newMachineSource('pool'), name: 'new-pool' }],
    })
    expect(result.issues).toEqual([])
    expect(parse(result.yaml)).toMatchObject({
      machine_sources: [{ machine_pool_name: 'advanced-pool' }, { machine_pool_name: 'new-pool' }],
    })
    expect(nodeSource(result.yaml, ['machine_sources', 0])).toBe(
      nodeSource(sources, ['machine_sources', 1]),
    )
  })

  it('does not insert defaults or rewrite unrelated fields when editing a machine row', () => {
    const source = `${header}machine_sources:\n  - machine_pool_name: pool\n    env_overlay: null\n    cwd: '/old'\n`
    const { session, draft } = draftFor(source)
    const result = session.evaluate({
      ...draft,
      machineSources: draft.machineSources.map((row) => ({ ...row, defaultCwd: '/new' })),
    })
    expect(result.issues).toEqual([])
    expect(result.yaml).toContain('    env_overlay: null\n')
    expect(parse(result.yaml)).toHaveProperty(['machine_sources', 0], {
      machine_pool_name: 'pool',
      env_overlay: null,
      cwd: '/new',
    })
  })

  it.each(['\n', '\r\n'])(
    'retains document markers, comments, and sibling bytes with %j line endings',
    (newline) => {
      const source =
        `# header\n---\n${header}tools:\n  web_search:\n    deferred: true\n  future: {data: [a,b,c]} # keep\n...\n# footer\n`.replaceAll(
          '\n',
          newline,
        )
      const { session, draft } = draftFor(source)
      const result = session.evaluate({
        ...draft,
        tools: draft.tools.map((tool) => ({ ...tool, deferred: false })),
      })
      expect(result.issues).toEqual([])
      expect(parse(result.yaml)).toHaveProperty(['tools', 'web_search'], {})
      expect(result.yaml).toContain(`  future: {data: [a,b,c]} # keep${newline}`)
      expect(result.yaml.startsWith(`# header${newline}---${newline}`)).toBe(true)
      expect(result.yaml.endsWith(`...${newline}# footer${newline}`)).toBe(true)
    },
  )

  it.each([
    'tools: {web_search: {}, future: {data: [a,b,c]},}\n',
    'tools: {web_search: {}, future: {data: [a,b,c]}, # keep comma, in comment\n}\n',
    'tools:\n  web_search: {}\n  # preserve opaque explanation\n  future: {data: [a,b,c]}\n',
  ])('preserves collection trivia when deleting and adding tools: %s', (tools) => {
    const { session, draft } = draftFor(header + tools)
    const result = session.evaluate({ ...draft, tools: [{ name: 'web_fetch', permission: null }] })
    expect(result.issues).toEqual([])
    expect(result.yaml).toContain('future: {data: [a,b,c]}')
    expect(parse(result.yaml)).toHaveProperty('tools', {
      future: { data: ['a', 'b', 'c'] },
      web_fetch: { type: 'built_in' },
    })
    if (tools.includes('# keep')) expect(result.yaml).toContain('# keep comma, in comment')
    if (tools.includes('# preserve')) expect(result.yaml).toContain('# preserve opaque explanation')
  })
})

it('preserves non-round memory values and list comments on unrelated edits', () => {
  const source = `${header}machine_sources:
  - machine_pool_name: pool
    machine_memory_mb: 1500
skills:
  - skl_aaaaaaaaaaaaaaaaaaaaaaaaaa # first comment
  - skl_bbbbbbbbbbbbbbbbbbbbbbbbbb # second comment
`
  const { session, draft } = draftFor(source)
  const result = session.evaluate({
    ...draft,
    machineSources: draft.machineSources.map((row) => ({ ...row, name: 'renamed-pool' })),
    skillIds: [...draft.skillIds, 'skl_cccccccccccccccccccccccccc'],
  })
  expect(result.issues).toEqual([])
  expect(result.yaml).toContain('    machine_memory_mb: 1500\n')
  expect(result.yaml).toContain(
    '  - skl_aaaaaaaaaaaaaaaaaaaaaaaaaa # first comment\n  - skl_bbbbbbbbbbbbbbbbbbbbbbbbbb # second comment\n',
  )
  expect(parse(result.yaml)).toHaveProperty('skills', [
    'skl_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    'skl_bbbbbbbbbbbbbbbbbbbbbbbbbb',
    'skl_cccccccccccccccccccccccccc',
  ])
})

it.each(['010', '1_0'])(
  'preserves version-dependent YAML numbers without blocking other edits: %s',
  (value) => {
    const source = `${header}max_subagents: ${value}\n`
    const { session, draft } = draftFor(source)
    expect(draft.maxSubagents).toBe('')
    expect(session.diagnostics.some((issue) => issue.message.includes('version-dependent'))).toBe(
      true,
    )
    const result = session.evaluate({ ...draft, instruction: 'Changed.' })
    expect(result.issues).toEqual([])
    expect(result.yaml).toContain(`max_subagents: ${value}\n`)
    expect(parse(result.yaml)).toHaveProperty('instruction', 'Changed.')
  },
)

it('preserves raw definition text, including folded styles and comment spacing', () => {
  const raw = `# Explanation

type: custom
description: >-
  First line
  second line.
input_schema: {type: object, properties: {query: {type: string}}, required: [query]}
`
  const source = `${header}tools:\n  example:\n${raw
    .split('\n')
    .map((line) => (line ? '    ' + line : ''))
    .join('\n')}\n`
  const { session, draft } = draftFor(source)
  const entry = draft.advancedEntries?.[0]
  expect(entry?.source.trimEnd()).toBe(raw.trimEnd())
  const result = session.evaluate({
    ...draft,
    advancedEntries: draft.advancedEntries?.map((entry) => ({
      ...entry,
      source: entry.source.replace('First line', 'Updated line'),
    })),
  })
  expect(result.issues).toEqual([])
  expect(result.yaml).toContain('# Explanation\n\n')
  expect(result.yaml).toContain('description: >-\n      Updated line\n      second line.')
  expect(result.yaml).toContain('required: [query]')
})

it('keeps edited JSON valid JSON, including newly added tools', () => {
  const source = JSON.stringify(
    {
      instruction: 'Help.',
      model: { provider_config: 'example', name: 'primary' },
      tools: {
        example: { type: 'custom', description: 'Example', input_schema: { type: 'object' } },
      },
    },
    null,
    2,
  )
  const { session, draft } = draftFor(source)
  const result = session.evaluate({
    ...draft,
    instruction: 'Changed.',
    tools: [{ name: 'web_search', permission: null }],
  })
  expect(result.issues).toEqual([])
  expect(() => {
    JSON.parse(result.yaml)
  }).not.toThrow()
  expect(JSON.parse(result.yaml)).toHaveProperty(['tools', 'web_search'], { type: 'built_in' })
  expect(nodeSource(result.yaml, ['tools', 'example'])).toBe(
    nodeSource(source, ['tools', 'example']),
  )
})

it('keeps explicit empty collections unchanged on a no-op save', () => {
  const source = `${header}machine_sources: []\nskills: []\nmemory_stores: []\ntools: {}\nmcp: {}\n`
  const { session, draft } = draftFor(source)
  expect(session.apply(draft)).toBe(source)
})

it('reorders managed machine rows without moving or rewriting an opaque row', () => {
  const { session, draft } = draftFor(sources)
  const result = session.evaluate({ ...draft, machineSources: [...draft.machineSources].reverse() })
  expect(result.issues).toEqual([])
  expect(parse(result.yaml)).toHaveProperty(
    ['machine_sources', 0, 'machine_name'],
    'managed-machine',
  )
  expect(parse(result.yaml)).toHaveProperty(
    ['machine_sources', 2, 'machine_pool_name'],
    'managed-pool',
  )
  expect(nodeSource(result.yaml, ['machine_sources', 1])).toBe(
    nodeSource(sources, ['machine_sources', 1]),
  )
})

it('distinguishes string and number permission parameters when editing a scalar', () => {
  const source = `${header}tools: {web_search: {permission: {mode: always_allow, parameters: {count: "5"}}}}\n`
  const { session, draft } = draftFor(source)
  const result = session.evaluate({
    ...draft,
    tools: draft.tools.map((tool) => ({
      ...tool,
      permission: { mode: 'always_allow', parameters: { count: 5 } },
    })),
  })
  expect(result.issues).toEqual([])
  expect(parse(result.yaml)).toHaveProperty(
    ['tools', 'web_search', 'permission', 'parameters', 'count'],
    5,
  )
})
