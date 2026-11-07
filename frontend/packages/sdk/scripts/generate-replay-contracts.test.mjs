import { describe, expect, it } from 'vitest'

import { replayContracts } from './generate-replay-contracts.mjs'

describe('OpenAPI replay contracts', () => {
  it('resolves path and operation header parameters, keeping method/path identity', () => {
    const key = { in: 'header', name: 'Idempotency-Key' }
    expect(
      replayContracts({
        components: {
          parameters: { 'escaped/name': { $ref: '#/components/parameters/key' }, key },
        },
        paths: {
          '/keyed': {
            post: { parameters: [{ $ref: '#/components/parameters/escaped~1name' }] },
            delete: {},
          },
          '/inherited': { parameters: [{ in: 'header', name: 'idempotency-key' }], put: {} },
          '/natural': { post: { 'x-omnara-idempotent': true } },
          '/read': { get: {} },
          '/unclassified': { post: {}, put: {} },
        },
      }),
    ).toEqual({
      known: [
        'DELETE /keyed',
        'GET /read',
        'POST /keyed',
        'POST /natural',
        'POST /unclassified',
        'PUT /inherited',
        'PUT /unclassified',
      ],
      keyed: ['POST /keyed', 'PUT /inherited'],
      idempotent: ['POST /natural'],
    })
  })

  it('records templates for read and unsafe write labels without granting replay safety', () => {
    expect(
      replayContracts({
        paths: {
          '/orgs/{orgID}/agents': {
            summary: 'Agents',
            parameters: [{ in: 'path', name: 'orgID' }],
            get: {},
            post: {},
          },
        },
      }),
    ).toEqual({
      known: ['GET /orgs/{orgID}/agents', 'POST /orgs/{orgID}/agents'],
      keyed: [],
      idempotent: [],
    })
  })

  it.each([
    { post: { 'x-omnara-idempotent': false } },
    { post: { 'x-omnara-idempotent': 'true' } },
    { get: { 'x-omnara-idempotent': true } },
    { head: { 'x-omnara-idempotent': true } },
    {
      parameters: [{ in: 'header', name: 'Idempotency-Key' }],
      post: { 'x-omnara-idempotent': true },
    },
  ])('rejects ambiguous or redundant declarations: %j', (item) => {
    expect(() => replayContracts({ paths: { '/invalid': item } })).toThrow('x-omnara-idempotent')
  })

  it.each(['#/components/parameters/Missing', 'remote.yaml#/Parameter'])(
    'fails closed on an unresolved reference: %s',
    ($ref) => {
      expect(() =>
        replayContracts({ paths: { '/invalid': { post: { parameters: [{ $ref }] } } } }),
      ).toThrow('parameter')
    },
  )
})
