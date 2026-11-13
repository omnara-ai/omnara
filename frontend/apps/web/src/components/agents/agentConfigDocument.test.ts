import { parseConfigDocument } from '@omnara/sdk/config-source'
import { describe, expect, it } from 'vitest'
import { isNode, isScalar, isSeq, parse, parseDocument } from 'yaml'

import { editConfigDocument } from './agentConfigDocument'
import { createBasicConfigSession } from './agentConfigSession'
import { configJson } from './agentConfigValidation'
import { newMachineSource } from './useAgentBuilderForm'

const header = 'instruction: Help.\nmodel: {provider_config: example, name: primary}\n'
const sources = `${header}machine_sources:
  - machine_pool_name: managed-pool
    cwd: '/original' # managed comment
  - machine_pool_name: advanced-pool
    machine_provider_options_overlay: {future_option: 'unchanged'} # opaque comment
  - machine_name: managed-machine
`

function draftFor(source: string, format: 'yaml' | 'json' = 'yaml') {
  const session = createBasicConfigSession(source, format)
  if (!session.initialDraft) throw new Error(JSON.stringify(session.diagnostics))
  return { session, draft: session.initialDraft }
}

function nodeValue(source: string, path: (string | number)[]) {
  const node = parseDocument(source).getIn(path, true)
  if (!isNode(node)) throw new Error('Missing node')
  return configJson.parse(node.toJSON())
}

it.each(['tools: {web_search: {}}\n', 'tools:\n  web_search: {}\n', 'tools: {}\n'])(
  'applies managed removals, raw additions, then managed additions: %s',
  (tools) => {
    const { session, draft } = draftFor(
      header + '# Keep tools here.\n' + tools + '# Keep skills here.\nskills: []\n',
    )
    const result = session.evaluate({
      ...draft,
      tools: [{ name: 'web_fetch', permission: null }],
      advancedEntries: [
        {
          path: ['tools', 'example'],
          kind: 'custom',
          source: 'type: custom\ndescription: Example.\ninput_schema: {type: object}\n',
        },
      ],
    })
    expect(result.issues).toEqual([])
    expect(parse(result.yaml)).toHaveProperty('tools', {
      web_fetch: { type: 'built_in' },
      example: { type: 'custom', description: 'Example.', input_schema: { type: 'object' } },
    })
    expect(result.yaml).toContain('# Keep tools here.')
    expect(result.yaml.endsWith('# Keep skills here.\nskills: []\n')).toBe(true)
  },
)

it.each([
  'tools:\n  web_search: {}\n  # Keep this tool comment.\n  old_tool: {type: custom, description: Example., input_schema: {type: object}} # inline\n',
  'tools: {web_search: {}, "old_tool": {type: custom, description: Example., input_schema: {type: object}}}\n',
  '{"tools":{"web_search":{},"old_tool":{"type":"custom","description":"Example.","input_schema":{"type":"object"}}}}',
])('renames a custom key without changing sibling values: %s', (source) => {
  const { session, draft } = draftFor(source)
  const result = session.evaluate({
    ...draft,
    advancedEntries: draft.advancedEntries?.map((entry) => ({
      ...entry,
      originalPath: entry.path,
      path: ['tools', 'true'],
    })),
  })
  expect(result.issues).toEqual([])
  expect(parse(result.yaml)).toEqual({
    tools: {
      web_search: {},
      true: { type: 'custom', description: 'Example.', input_schema: { type: 'object' } },
    },
  })
  if (source.includes('# Keep this tool comment.'))
    expect(result.yaml).toContain('# Keep this tool comment.')
  expect(createBasicConfigSession(result.yaml, 'yaml').initialDraft).not.toBeNull()
  expect(session.apply(draft)).toBe(source)
})

describe('document ownership', () => {
  it('keeps rename identity distinct from a new tool reusing its original name', () => {
    const source =
      'tools: {old_tool: {type: custom, description: Example., input_schema: {type: object}}}\n'
    const { session, draft } = draftFor(source)
    const original = draft.advancedEntries?.[0]
    if (!original) throw new Error('Missing tool')
    const renamed = { ...original, path: ['tools', 'renamed_tool'] }
    const added = { path: original.path, kind: 'custom' as const, source: original.source }
    const result = session.evaluate({ ...draft, advancedEntries: [renamed, added] })
    const definition: unknown = parse(original.source)
    expect(result.issues).toEqual([])
    expect(parse(result.yaml)).toHaveProperty('tools', {
      renamed_tool: definition,
      old_tool: definition,
    })
    const removed = session.evaluate({
      ...draft,
      advancedEntries: [{ ...renamed, removed: true }, added],
    })
    expect(removed.issues).toEqual([])
    expect(parse(removed.yaml)).toEqual(parse(source))
  })

  it.each([false, true])(
    'releases a custom name for a managed tool after explicit removal or rename (%s)',
    (removed) => {
      const source =
        header +
        'tools: {web_search: {type: custom, description: Example., input_schema: {type: object}}}\n'
      const { session, draft } = draftFor(source)
      const result = session.evaluate({
        ...draft,
        tools: [{ name: 'web_search', permission: null }],
        advancedEntries: draft.advancedEntries?.map((entry) => ({
          ...entry,
          path: ['tools', 'lookup_item'],
          removed,
        })),
      })
      expect(result.issues).toEqual([])
      expect(result.unappliedChanges).toBe(false)
      expect(parse(result.yaml)).toHaveProperty(['tools', 'web_search'], { type: 'built_in' })
      expect(parseDocument(result.yaml).hasIn(['tools', 'lookup_item'])).toBe(!removed)
    },
  )

  it('rejects edits to a preserved path or its parent', () => {
    const source = `${header}tools: {managed: {}, opaque: {future: true}}\n`
    for (const path of [['tools'], ['tools', 'opaque'], ['tools', 'opaque', 'future']]) {
      const transaction = editConfigDocument(
        source,
        parseConfigDocument(source, 'yaml').doc,
        [['tools', 'opaque']],
        'yaml',
      )
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
    const transaction = editConfigDocument(
      source,
      parseConfigDocument(source, 'yaml').doc,
      [['tools', 'opaque']],
      'yaml',
    )
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
    expect(nodeValue(result.yaml, ['machine_sources', 1])).toEqual(
      nodeValue(sources, ['machine_sources', 1]),
    )
    expect(session.apply(draft)).toBe(sources)
  })

  it('removes and appends managed rows while retaining opaque row identity and values', () => {
    const { session, draft } = draftFor(sources)
    const result = session.evaluate({
      ...draft,
      machineSources: [{ ...newMachineSource('pool'), name: 'new-pool' }],
    })
    expect(result.issues).toEqual([])
    expect(parse(result.yaml)).toMatchObject({
      machine_sources: [{ machine_pool_name: 'advanced-pool' }, { machine_pool_name: 'new-pool' }],
    })
    expect(nodeValue(result.yaml, ['machine_sources', 0])).toEqual(
      nodeValue(sources, ['machine_sources', 1]),
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

it('keeps model controls available beside an opaque model option', () => {
  const source = `instruction: Help.
model:
  provider_config: example
  name: primary
  reasoning: {effort: high}
  context_window_tokens: 200_000
`
  const { session, draft } = draftFor(source)
  expect(draft).toMatchObject({
    providerConfig: 'example',
    modelName: 'primary',
    reasoningEffort: 'high',
  })
  expect(draft.advancedEntries?.map((entry) => entry.path)).toEqual([
    ['model', 'context_window_tokens'],
  ])
  const result = session.evaluate({ ...draft, modelName: 'secondary', reasoningEffort: 'low' })
  expect(result.issues).toEqual([])
  expect(parse(result.yaml)).toEqual(
    parse(source.replace('primary', 'secondary').replace('high', 'low')),
  )
})

it('keeps the model opaque when a managed field has version-dependent meaning', () => {
  const source = header.replace('name: primary', 'name: 2026-01-02')
  const { session, draft } = draftFor(source)
  expect(draft.advancedEntries?.map((entry) => entry.path)).toEqual([['model']])
  expect(draft.modelName).toBe('')
  const updated = session.apply({ ...draft, instruction: 'Changed.' })
  expect(parse(updated)).toEqual(parse(source.replace('Help.', 'Changed.')))
  expect(updated).toContain('name: 2026-01-02')
})

it('reorders managed machine rows without moving or changing an opaque row', () => {
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
  expect(nodeValue(result.yaml, ['machine_sources', 1])).toEqual(
    nodeValue(sources, ['machine_sources', 1]),
  )
})

it.each(['tools: {web_search}', 'mcp: {docs}', 'subagents: {helper}', 'max_depth:'])(
  'reports value-less entries without throwing: %s',
  (entry) => {
    const source = header + entry + '\n'
    const session = createBasicConfigSession(source, 'yaml')
    expect(session.initialDraft).not.toBeNull()
    expect(session.diagnostics.some((issue) => issue.error)).toBe(true)
    if (session.initialDraft) expect(session.apply(session.initialDraft)).toBe(source)
  },
)

it.each(['2026-01-02', '2026-1-2', '1_000', '0b1', '-0x1F', '0x1_F', '0X1F', '-0o17', '1e_3'])(
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
      createBasicConfigSession('', 'yaml').evaluate(changed),
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
      expect(createBasicConfigSession(result.yaml, 'yaml').diagnostics).toEqual([])
    }
  },
)

it.each(['2026-1-2', '-0x1F', '0x1_F', '0X1F', '-0o17', '1e_3'])(
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
  expect(nodeValue(result.yaml, ['mcp', 'docs'])).toEqual(nodeValue(source, ['mcp', 'docs']))
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

it('explains explicit YAML 1.1 documents without rewriting them', () => {
  const source = '%YAML 1.1\n---\n' + header
  const session = createBasicConfigSession(source, 'yaml')
  expect(session.initialDraft).toBeNull()
  expect(session.diagnostics.some((issue) => issue.kind === 'fatal')).toBe(true)
  expect(session.diagnostics.map((issue) => issue.message).join('\n')).toContain(
    'declares YAML 1.1',
  )
})

it.each([
  [header, false],
  [header, true],
  [header + 'tools: {}\n', false],
] as const)(
  'adds custom tools beside a new built-in with a block definition (%#)',
  (source, reopen) => {
    let { session, draft } = draftFor(source)
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
    expect(result.yaml).toContain('# retain schema comment')
    expect(parse(result.yaml)).toHaveProperty(['tools', 'web_search'])
    expect(parse(result.yaml)).toHaveProperty(['tools', 'example'], parse(raw))
  },
)

it.each(['# Updated\n  ', ''])(
  'applies raw comment-only edits inside inline maps (%#)',
  (comment) => {
    const source =
      'tools: {example: # Original\n  {type: custom, description: Example., input_schema: {type: object}}}\n'
    const { session, draft } = draftFor(source)
    const result = session.evaluate({
      ...draft,
      advancedEntries: draft.advancedEntries?.map((entry) => ({
        ...entry,
        source: entry.source.replace('# Original\n', comment.trimEnd() ? '# Updated\n' : ''),
      })),
    })
    expect(result.issues).toEqual([])
    expect(parse(result.yaml)).toEqual(parse(source))
    expect(result.yaml).not.toContain('# Original')
    expect(result.yaml.includes('# Updated')).toBe(comment !== '')
  },
)

it('keeps invalid numeric drafts out of the source while applying other edits', () => {
  const original =
    header +
    'machine_sources: [{machine_pool_name: pool, machine_memory_mb: 1024}]\nsubagents: {helper: {type: self, max_instances: 2}}\nmax_depth: 2\n'
  const paths = [
    ['max_depth'],
    ['machine_sources', '0', 'machine_memory_mb'],
    ['subagents', 'helper', 'max_instances'],
  ]
  const { draft } = draftFor(original)
  for (const source of [original, '']) {
    const result = createBasicConfigSession(source, 'yaml').evaluate({
      ...draft,
      instruction: 'Pending instruction.',
      maxDepth: 'abc',
      machineSources: draft.machineSources.map((row) => ({ ...row, machineMemoryGb: '1e308' })),
      subagents: draft.subagents.map((row) => ({ ...row, maxInstances: 'Infinity' })),
    })
    expect(result.issues).toEqual(
      paths.map((path) => ({
        path,
        kind: 'advanced',
        error: true,
        message: 'Enter a valid number.',
      })),
    )
    expect(result.unappliedChanges).toBe(true)
    expect(parse(result.yaml)).toHaveProperty('instruction', 'Pending instruction.')
    for (const path of paths)
      expect(parseDocument(result.yaml).getIn(path)).toBe(
        source ? parseDocument(original).getIn(path) : undefined,
      )
  }
})

it.each(['<<', '10.0.0.1'])(
  'quotes mapping keys only when their meaning requires it: %s',
  (text) => {
    const { session, draft } = draftFor(header + 'machine_sources: [{machine_name: example}]\n')
    const result = session.evaluate({
      ...draft,
      machineSources: draft.machineSources.map((row) => ({
        ...row,
        envRows: [{ id: 'example', key: text, value: text }],
      })),
    })
    const written = text === '<<' ? '"<<"' : text
    expect(result.issues).toEqual([])
    expect(result.yaml).toContain(`${written}: ${written}`)
    expect(createBasicConfigSession(result.yaml, 'yaml').diagnostics).toEqual([])
  },
)

it.each(['yaml', 'json'] as const)(
  'normalizes edited %s while preserving exact schema numbers and unknown data',
  (format) => {
    const numbers = [
      '9007199254740993',
      '18446744073709551615',
      '0.123456789012345678901',
      '1.234567890123456789e+40',
      ...(format === 'json'
        ? []
        : ['010', '1_0', '0x1_F', '-0x1F', '-0o17', '0b1', '1e_3', '2026-1-2']),
    ]
    const written = (text: string, path: string[]) => {
      const list = parseDocument(text, { schema: 'failsafe' }).getIn([...path, 'enum'], true)
      return isSeq(list)
        ? list.items.map((item) => isScalar(item) && item.type === 'PLAIN' && item.source)
        : []
    }
    const schema = ['input_schema', 'properties', 'value']
    const source =
      format === 'json'
        ? '{"instruction":"Help.","tools":{"example":{"type":"custom","description":"Example.","input_schema":{"type":"object","properties":{"value":{"enum":[' +
          numbers.join(',') +
          ']}}}}},"future":{"nested":{"__proto__":"retained"}}}'
        : 'instruction: Help.\n# extension notes\nfuture: {nested: {__proto__: retained}}\ntools: {example: {type: custom, description: Example., input_schema: {type: object, properties: {value: {enum: [' +
          numbers.join(', ') +
          ']}}}}}\n'
    const { session, draft } = draftFor(source, format)
    expect(session.apply(draft)).toBe(source)
    const definition = draft.advancedEntries?.find((entry) => entry.kind === 'custom')?.source
    expect(written(definition ?? '', schema)).toEqual(numbers)
    const result = session.evaluate({ ...draft, instruction: 'Changed.\nSecond line.' })
    expect(result.issues).toEqual([])
    expect(parse(result.yaml)).toEqual({ ...parse(source), instruction: 'Changed.\nSecond line.' })
    expect(written(result.yaml, ['tools', 'example', ...schema])).toEqual(numbers)
    if (format === 'json') expect(JSON.parse(result.yaml)).toEqual(parse(result.yaml))
    else expect(result.yaml).toContain('# extension notes')
    const reopened = draftFor(result.yaml, format)
    expect(reopened.session.apply(reopened.draft)).toBe(result.yaml)
  },
)

it('inserts a block custom definition into an inline document without changing its meaning', () => {
  const { session, draft } = draftFor('{instruction: Help., tools: {web_search: {}}}\n')
  const definition =
    'type: custom\ndescription: Example.\ninput_schema:\n  type: object\n  properties:\n    value: {const: 9007199254740993}\n# definition notes\n'
  const result = session.evaluate({
    ...draft,
    advancedEntries: [{ path: ['tools', 'example'], kind: 'custom', source: definition }],
  })
  expect(result.issues).toEqual([])
  expect(parse(result.yaml)).toEqual({
    instruction: 'Help.',
    tools: { web_search: {}, example: configJson.parse(parse(definition)) },
  })
  expect(result.yaml).toContain('9007199254740993')
  expect(result.yaml).toContain('# definition notes')
})

it.each([
  '[A, # first\n B # second\n]',
  '[A, B # second\n # continued\n]',
  '[\n # first\n A,\n # second\n B\n]',
])('preserves sequence values and comment content through normalization and reload: %s', (list) => {
  const source =
    'skills: ' +
    list
      .replaceAll('A', 'skl_aaaaaaaaaaaaaaaaaaaaaaaaaa')
      .replaceAll('B', 'skl_bbbbbbbbbbbbbbbbbbbbbbbbbb') +
    '\n'
  const original = parseDocument(source)
  let { session, draft } = draftFor(source)
  for (const order of [
    [1, 0],
    [0, 1],
  ]) {
    const result = session.evaluate({ ...draft, skillIds: [...draft.skillIds].reverse() })
    expect(result.issues).toEqual([])
    for (const [index, originalIndex] of order.entries()) {
      const before = original.getIn(['skills', originalIndex], true)
      const after = parseDocument(result.yaml).getIn(['skills', index], true)
      if (!isNode(before) || !isNode(after)) throw new Error('Missing skill node')
      expect(after.toJSON()).toBe(before.toJSON())
      for (const comment of source.match(/#[^\n]*/g) ?? []) expect(result.yaml).toContain(comment)
    }
    ;({ session, draft } = draftFor(result.yaml))
  }
})

it.each(['\n', '\r\n'])(
  'preserves document and neighboring section comments with line ending %j',
  (lineEnding) => {
    const source = (
      '# header\n---\n' +
      header +
      'tools: {web_search: {},\n # opaque notes\n future: {data: [a,b,c]}}\n# skill notes\nskills: []\n...\n# footer\n'
    ).replaceAll('\n', lineEnding)
    const { session, draft } = draftFor(source)
    const result = session.evaluate({ ...draft, tools: [{ name: 'web_fetch', permission: null }] })
    expect(result.issues).toEqual([])
    expect(parse(result.yaml)).toEqual({
      ...parse(source),
      tools: { future: { data: ['a', 'b', 'c'] }, web_fetch: { type: 'built_in' } },
    })
    for (const comment of ['# header', '# opaque notes', '# skill notes', '# footer'])
      expect(result.yaml).toContain(comment)
  },
)

it('keeps neighboring tool comments through repeated saves and removal', () => {
  let source =
    header +
    `tools:
  web_search: {type: built_in} # search notes
  # fetch notes
  web_fetch: {type: built_in, deferred: true}
# skill notes
skills: []
`
  for (const instruction of ['First edit.', 'Second edit.']) {
    const { session, draft } = draftFor(source)
    source = session.apply({ ...draft, instruction })
  }
  const { session, draft } = draftFor(source)
  const result = session.evaluate({
    ...draft,
    tools: draft.tools.filter((tool) => tool.name !== 'web_search'),
  })
  expect(result.issues).toEqual([])
  expect(result.yaml).toContain('# fetch notes')
  expect(result.yaml).toContain('# skill notes')
  expect(result.yaml).not.toContain('# search notes')
  expect(parse(result.yaml)).toHaveProperty('tools', {
    web_fetch: { type: 'built_in', deferred: true },
  })
})

it('refuses serialization that changes a value and retains the original source', () => {
  const source = header + 'machine_sources: [{machine_name: example}]\n'
  const { session, draft } = draftFor(source)
  const result = session.evaluate({
    ...draft,
    machineSources: draft.machineSources.map((row) => ({
      ...row,
      envRows: [{ id: 'example', key: 'VALUE', value: ' \n' }],
    })),
  })
  expect(result.unappliedChanges).toBe(true)
  expect(result.issues).toMatchObject([{ kind: 'fatal', error: true }])
  expect(result.yaml).toBe(source)
})

it.each(['yaml', 'json'] as const)('handles Unicode line separators safely in %s', (format) => {
  const definition = {
    type: 'custom',
    description: 'Before\u0085middle\u2028after\u2029end',
    input_schema: { type: 'object' },
  }
  const source =
    format === 'json'
      ? JSON.stringify({ tools: { example: definition } })
      : 'tools: {example: {type: custom, description: "Before\\Nmiddle\\Lafter\\Pend", input_schema: {type: object}}}\n'
  const session = createBasicConfigSession(source, format)
  if (format === 'yaml') {
    expect(session.initialDraft).toBeNull()
    expect(session.diagnostics).toMatchObject([
      { path: ['tools', 'example', 'description'], kind: 'fatal' },
    ])
    expect(session.diagnostics[0]?.message).toContain('Unicode line separator')
    return
  }
  const { draft } = draftFor(source, format)
  expect(session.apply(draft)).toBe(source)
  const result = session.evaluate({ ...draft, instruction: definition.description })
  expect(result.issues).toEqual([])
  expect(JSON.parse(result.yaml)).toEqual({
    instruction: definition.description,
    tools: { example: definition },
  })
})

it('blocks new YAML strings with unsafe line separators without losing source', () => {
  const { session, draft } = draftFor(header)
  const result = session.evaluate({
    ...draft,
    instruction: 'Before\u0085middle\u2028after\u2029end',
  })
  expect(result.yaml).toBe(header)
  expect(result.unappliedChanges).toBe(true)
  expect(result.issues).toMatchObject([{ path: ['instruction'], kind: 'fatal', error: true }])
  expect(result.issues[0]?.message).toContain('Unicode line separator')
})

it.each([
  '# note\u0085hidden: true\n' + header,
  header + '# note\u2028hidden: true\n',
  header + 'tools: {} # note\u2029hidden: true\n',
])('detects Unicode line separators in comments: %j', (source) => {
  const session = createBasicConfigSession(source, 'yaml')
  expect(session.initialDraft).toBeNull()
  expect(
    session.diagnostics.some((issue) => issue.message.includes('Unicode line separator')),
  ).toBe(true)
})
