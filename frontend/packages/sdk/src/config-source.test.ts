import { expect, it } from 'vitest'
import { isScalar, parse } from 'yaml'

import { parseConfigDocument, serializeConfigDocument } from './config-source'

it.each(['yaml', 'json'] as const)('serializes ordinary document edits in %s', (format) => {
  const { doc } = parseConfigDocument(
    format === 'json'
      ? '{"max_subagents":1,"nested":{"count":2},"values":[3,9007199254740993,0.123456789012345678901]}'
      : 'max_subagents: 1\nnested: {count: 2}\nvalues: [3, 9007199254740993, 0.123456789012345678901]\n',
    format,
  )
  doc.set('max_subagents', 4)
  doc.setIn(['nested', 'count'], '2')
  doc.setIn(['values', 0], 6)
  const source = serializeConfigDocument(doc, format)
  const result: unknown = format === 'json' ? JSON.parse(source) : parse(source)
  expect(result).toMatchObject({ max_subagents: 4, nested: { count: '2' } })
  expect(result).toHaveProperty(['values', 0], 6)
  expect(source).toContain('9007199254740993')
  expect(source).toContain('0.123456789012345678901')
  expect(doc.getIn(['values', 0])).toBe(6)
})

it('retains unchanged timestamp text but writes a mutated timestamp value', () => {
  const { doc } = parseConfigDocument('date: !!timestamp 2026-01-02T03:04:05+05:30\n', 'yaml')
  expect(serializeConfigDocument(doc, 'yaml')).toContain('2026-01-02T03:04:05+05:30')
  const node = doc.get('date', true)
  if (!isScalar(node) || !(node.value instanceof Date)) throw new Error('Expected a timestamp')
  node.value.setUTCFullYear(2027)
  expect(parseConfigDocument(serializeConfigDocument(doc, 'yaml'), 'yaml').doc.get('date')).toEqual(
    node.value,
  )
})
