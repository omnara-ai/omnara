import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { gunzipSync } from 'node:zlib'

import { buildSchema, getOperationAST, getVariableValues, parse, validate } from 'graphql'
import { describe, expect, it } from 'vitest'

import { controlRepositoryQuery } from './control-protocol'
import { githubDocuments } from './documents'

// Full native schema rather than a handwritten approximation. Compression keeps
// the checked-in fixture small; no network access is needed during tests.
// Source: https://docs.github.com/public/fpt/schema.docs.graphql (2026-09-15).
const raw = gunzipSync(readFileSync(new URL('./testdata/schema.docs.graphql.gz', import.meta.url)))
const schema = buildSchema(raw.toString('utf8'))

describe('GitHub native GraphQL contract', () => {
  it('validates the minimal control identity query against the real native schema', () => {
    expect(validate(schema, parse(controlRepositoryQuery))).toEqual([])
  })
  it('pins the exact public schema bytes', () => {
    expect(createHash('sha256').update(raw).digest('hex')).toBe(
      '8ecdb21a5c3affdeaa0e55bd9174536aa6c69cbb61f20ec796085aa1509c95df',
    )
  })
  it.each(Object.entries(githubDocuments))(
    'validates %s against the real schema',
    (_name, document) => {
      expect(validate(schema, parse(document)).map((error) => error.message)).toEqual([])
    },
  )
  it('validates timeline input and rejects invented mutation fields', () => {
    const operation = getOperationAST(parse(githubDocuments.timelineComment))
    const variables = { input: { subjectId: 'PR_1', body: 'Original text' } }
    expect(
      getVariableValues(schema, operation?.variableDefinitions ?? [], variables).errors,
    ).toBeUndefined()
    const invalid = { input: { ...variables.input, path: 'main.ts' } }
    expect(
      getVariableValues(schema, operation?.variableDefinitions ?? [], invalid).errors,
    ).toHaveLength(1)
  })
  it('keeps all review writes out of the fixed GraphQL catalog', () => {
    const mutations = Object.values(githubDocuments).filter((document) =>
      document.startsWith('mutation '),
    )
    expect(mutations).toEqual([githubDocuments.timelineComment])
  })
})
