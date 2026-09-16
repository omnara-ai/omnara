import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { gunzipSync } from 'node:zlib'

import { buildSchema, getOperationAST, getVariableValues, parse, validate } from 'graphql'
import { describe, expect, it } from 'vitest'

import { controlRepositoryQuery } from './control-protocol'
import { githubDocuments } from './documents'
import { githubFixture, json, oldCommit } from './test-support'

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
  it.each([
    ['createReview', { pullRequestId: 'PR_1', commitOID: oldCommit, body: 'marker' }],
    ['reviewSummary', { pullRequestId: 'PR_1', body: 'Summary', event: 'COMMENT' }],
    [
      'addFinding',
      {
        pullRequestReviewId: 'PRR_1',
        body: 'Line',
        path: 'main.ts',
        line: 12,
        side: 'RIGHT',
        subjectType: 'LINE',
      },
    ],
    [
      'addFinding',
      { pullRequestReviewId: 'PRR_1', body: 'File', path: 'main.ts', subjectType: 'FILE' },
    ],
    [
      'threadReply',
      { pullRequestReviewThreadId: 'PRRT_1', pullRequestReviewId: 'PRR_1', body: 'Reply' },
    ],
    ['submitReview', { pullRequestReviewId: 'PRR_1', event: 'COMMENT', body: 'Summary' }],
  ] as const)('coerces the actual native %s input', (name, input) => {
    const operation = getOperationAST(parse(githubDocuments[name]))
    expect(operation).toBeDefined()
    const result = getVariableValues(schema, operation?.variableDefinitions ?? [], { input })
    expect(result.errors).toBeUndefined()
  })
  it('permits native draft threads but rejects invented subjectType on DraftPullRequestReviewThread', () => {
    const operation = getOperationAST(parse(githubDocuments.createReview))
    const input = {
      pullRequestId: 'PR_1',
      threads: [{ body: 'Line', path: 'main.ts', line: 12, side: 'RIGHT' }],
    }
    expect(
      getVariableValues(schema, operation?.variableDefinitions ?? [], { input }).errors,
    ).toBeUndefined()
    const invalid = { ...input, threads: [{ ...input.threads[0], subjectType: 'FILE' }] }
    const result = getVariableValues(schema, operation?.variableDefinitions ?? [], {
      input: invalid,
    })
    expect(result.errors?.some((error) => error.message.includes('subjectType'))).toBe(true)
  })
  it('makes the HTTP fake enforce native variable coercion without rejecting valid Draft threads', async () => {
    const { url } = await githubFixture((_request, response) => {
      json(response, { data: {} })
    })
    const send = (subjectType?: string) => {
      const lineThread = { body: 'Line', path: 'main.ts', line: 12, side: 'RIGHT' }
      const thread = subjectType ? { ...lineThread, subjectType } : lineThread
      return fetch(`${url}/graphql`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({
          query: githubDocuments.createReview,
          variables: { input: { pullRequestId: 'PR_1', threads: [thread] } },
        }),
      })
    }
    expect((await send()).status).toBe(200)
    expect((await send('FILE')).status).toBe(400)
  })
  it('rejects a fictional review field on the thread and a string line number', () => {
    expect(
      validate(
        schema,
        parse(
          'query { node(id: "PRRT_1") { ... on PullRequestReviewThread { pullRequestReview { id } } } }',
        ),
      ),
    ).toHaveLength(1)
    const operation = getOperationAST(parse(githubDocuments.addFinding))
    const result = getVariableValues(schema, operation?.variableDefinitions ?? [], {
      input: {
        pullRequestReviewId: 'PRR_1',
        body: 'Line',
        path: 'main.ts',
        line: '12',
        side: 'RIGHT',
      },
    })
    expect(result.errors?.some((error) => error.message.includes('Int'))).toBe(true)
  })
})
