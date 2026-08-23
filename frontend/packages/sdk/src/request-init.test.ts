import { afterEach, describe, expect, it, vi } from 'vitest'

import { bearerToken } from './auth'
import { createOmnaraClient } from './client'

// Deno rejects a Request init containing its reserved `client` key unless the
// value is a Deno.HttpClient; Bun is similarly strict. Node ignores the key,
// so only a strict stand-in can catch the leak on this test runner
// (https://github.com/hey-api/hey-api/issues/4177).
class StrictRuntimeRequest extends Request {
  constructor(input: RequestInfo | URL, init?: RequestInit) {
    if (init != null && 'client' in init) {
      throw new TypeError("Failed to construct 'Request': Argument 2 `client` must be a Deno.HttpClient")
    }
    super(input, init)
  }
}

describe('request dispatch on strict runtimes', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('never leaks the per-call client selector into the Request init', async () => {
    vi.stubGlobal('Request', StrictRuntimeRequest)
    const fetchMock = vi.fn(async (request: Request) => {
      expect(request.headers.get('authorization')).toBe('Bearer test-token')
      return Response.json({ ok: true })
    })
    vi.stubGlobal('fetch', fetchMock)

    const client = createOmnaraClient({ baseUrl: 'https://api.test', auth: bearerToken('test-token') })
    // sdk functions forward their whole options object — the `client`
    // selector included — to the selected instance's dispatch method.
    const { data } = await client.get({ url: '/api/v1/me', ...({ client } as Record<string, unknown>) })
    expect(data).toEqual({ ok: true })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
