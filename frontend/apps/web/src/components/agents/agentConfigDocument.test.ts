import { describe, expect, it } from 'vitest'
import { isNode, isScalar, parse, parseDocument } from 'yaml'

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

  it('cannot insert raw definitions over existing nodes or under opaque parents', () => {
    const source = `${header}tools: {managed: {}, opaque: {future: true}}\n`
    const transaction = editConfigDocument(source, parseConfigDocument(source).doc, [
      ['tools', 'opaque'],
    ])
    for (const path of [
      ['tools', 'managed'],
      ['tools', 'opaque', 'new'],
    ]) {
      expect(() => {
        transaction.setRawDefinition(path, '{type: custom}')
      }).toThrow()
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

it('keeps explicit empty collections unchanged on a no-op save', () => {
  const source = `${header}machine_sources: []\nskills: []\nmemory_stores: []\ntools: {}\nmcp: {}\n`
  const { session, draft } = draftFor(source)
  expect(session.apply(draft)).toBe(source)
})

it('reorders managed machine rows without moving or rewriting an opaque row', () => {
  const { session, draft } = draftFor(sources.trimEnd())
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

it.each(['tools: {web_search}', 'mcp: {docs}', 'subagents: {helper}', 'max_depth:'])(
  'reports value-less entries without throwing: %s',
  (entry) => {
    const source = header + entry + '\n'
    const session = createBasicConfigSession(source)
    expect(session.initialDraft).not.toBeNull()
    expect(session.diagnostics.some((issue) => issue.error)).toBe(true)
    if (session.initialDraft) expect(session.apply(session.initialDraft)).toBe(source)
  },
)

it.each(['2026-01-02', '2026-1-2', '1_000', '0b1', '-0x1F', '0x1_F'])(
  'quotes strings consistently with backend scalar parsing: %s',
  (value) => {
    const { session, draft } = draftFor(header + 'machine_sources: [{machine_name: example}]\n')
    const changed = {
      ...draft,
      machineSources: draft.machineSources.map((row) => ({
        ...row,
        envRows: [{ id: 'example', key: 'VALUE', value }],
      })),
    }
    for (const result of [
      session.evaluate(changed),
      createBasicConfigSession('').evaluate(changed),
    ]) {
      expect(result.issues).toEqual([])
      expect(parse(result.yaml)).toHaveProperty(
        ['machine_sources', 0, 'env_overlay', 'VALUE'],
        value,
      )
      const scalar = parseDocument(result.yaml).getIn(
        ['machine_sources', 0, 'env_overlay', 'VALUE'],
        true,
      )
      expect(isScalar(scalar) && scalar.type !== 'PLAIN').toBe(true)
      expect(createBasicConfigSession(result.yaml).diagnostics).toEqual([])
    }
  },
)

it.each(['2026-1-2', '-0x1F', '0x1_F'])(
  'preserves backend-specific scalar forms on read: %s',
  (value) => {
    const source =
      header + `machine_sources: [{machine_name: example, env_overlay: {VALUE: ${value}}}]\n`
    const { session, draft } = draftFor(source)
    expect(draft.machineSources).toHaveLength(0)
    expect(session.diagnostics.some((issue) => issue.message.includes('version-dependent'))).toBe(
      true,
    )
    expect(session.apply({ ...draft, instruction: 'Updated.' })).toContain(`VALUE: ${value}`)
  },
)

it('retains unrelated Builder edits and the collision diagnostic', () => {
  const source = header + 'mcp: {docs: {url: "https://example.com", future: true}}\n'
  const { session, draft } = draftFor(source)
  const result = session.evaluate({
    ...draft,
    instruction: 'Updated.',
    mcpServers: [
      {
        id: 'new',
        name: 'docs',
        url: 'https://other.example.com',
        permission: null,
        defaultEnabled: true,
        authType: 'none',
        secretId: '',
        service: '',
        region: '',
        tools: [],
      },
    ],
  })
  expect(result.issues).toMatchObject([
    { path: ['mcp', 'docs'], message: 'This name is already used by a preserved definition.' },
  ])
  expect(parse(result.yaml)).toHaveProperty('instruction', 'Updated.')
  expect(nodeSource(result.yaml, ['mcp', 'docs'])).toBe(nodeSource(source, ['mcp', 'docs']))
})

it('changes a machine source kind without losing the sequence entry', () => {
  const { session, draft } = draftFor(header + 'machine_sources:\n  - machine_pool_name: pool\n')
  const result = session.evaluate({
    ...draft,
    machineSources: draft.machineSources.map((row) => ({
      ...newMachineSource('machine'),
      id: row.id,
      name: 'example',
    })),
  })
  expect(result.issues).toEqual([])
  expect(parse(result.yaml)).toHaveProperty('machine_sources', [{ machine_name: 'example' }])
})

it('preserves large schema integers and blocks conversions that would round them', () => {
  const source =
    header +
    'tools: {example: {type: custom, description: Example, input_schema: {type: object, properties: {value: {const: 9007199254740993}}}}}\n'
  const { session, draft } = draftFor(source)
  const result = session.evaluate({
    ...draft,
    advancedEntries: draft.advancedEntries?.map((entry) => ({
      ...entry,
      source: entry.source.replace('Example', 'Updated'),
    })),
  })
  expect(result.issues).toEqual([])
  expect(result.yaml).toContain('const: 9007199254740993')
  expect(result.yaml).toContain('description: Updated')
  const empty = '{instruction: Help.}\n'
  const transaction = editConfigDocument(empty, parseConfigDocument(empty).doc, [])
  expect(() => {
    transaction.setRawDefinition(
      ['tools', 'new_tool'],
      'type: custom\ndescription: Example\ninput_schema: {type: object, properties: {value: {const: 9007199254740993}}}',
    )
  }).toThrow('This inline collection requires a flow definition')
  expect(transaction.serialize()).toBe(empty)
})

it('explains explicit YAML 1.1 documents without rewriting them', () => {
  const source = '%YAML 1.1\n---\n' + header
  const session = createBasicConfigSession(source)
  expect(session.initialDraft).toBeNull()
  expect(session.diagnostics.some((issue) => issue.kind === 'fatal')).toBe(true)
  expect(session.diagnostics.map((issue) => issue.message).join('\n')).toContain(
    'declares YAML 1.1',
  )
})

it.each([false, true])(
  'adds custom tools beside a new built-in without creating an inline map (reopen: %s)',
  (reopen) => {
    let { session, draft } = draftFor(header)
    draft = { ...draft, tools: [{ name: 'web_search', permission: null }] }
    if (reopen) ({ session, draft } = draftFor(session.apply(draft)))
    const raw =
      'type: custom\ndescription: Example.\ninput_schema: # retain schema comment\n  type: object\n'
    const result = session.evaluate({
      ...draft,
      advancedEntries: [{ path: ['tools', 'example'], kind: 'custom', source: raw }],
    })
    expect(result.issues).toEqual([])
    expect(result.yaml).toContain('\ntools:\n')
    expect(result.yaml).toContain('input_schema: # retain schema comment\n')
    expect(parse(result.yaml)).toHaveProperty(['tools', 'web_search'])
    expect(parse(result.yaml)).toHaveProperty(['tools', 'example'], parse(raw))
  },
)
