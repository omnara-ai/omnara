import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { gunzipSync } from 'node:zlib'

import { buildSchema, getOperationAST, parse, validate } from 'graphql'
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
  it('keeps every fixed GraphQL document a query so the transport can retry reads safely', () => {
    for (const [name, document] of Object.entries(githubDocuments))
      expect(getOperationAST(parse(document))?.operation, name).toBe('query')
  })
})
