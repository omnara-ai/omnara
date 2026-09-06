/** @vitest-environment happy-dom */
import { describe, expect, it } from 'vitest'

import { emptyResponse, fakeApi } from './fake-api'

describe('fakeApi', () => {
  it('records the body of a Request object the way the SDK client sends it', async () => {
    const api = fakeApi([{ method: 'POST', path: '/api/v1/orgs', respond: () => emptyResponse() }])
    await api.fetch(
      new Request('http://localhost/api/v1/orgs', {
        method: 'POST',
        body: JSON.stringify({ name: 'Acme' }),
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    expect(api.requestsTo('POST', '/api/v1/orgs')).toMatchObject([{ body: { name: 'Acme' } }])
  })

  it('records the body of a url plus init call', async () => {
    const api = fakeApi([{ method: 'PUT', path: '/api/v1/x', respond: () => emptyResponse() }])
    await api.fetch('http://localhost/api/v1/x', { method: 'PUT', body: JSON.stringify([1]) })
    expect(api.requestsTo('PUT', '/api/v1/x')).toMatchObject([{ body: [1] }])
  })
})
