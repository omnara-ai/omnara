import { createOmnaraClient } from '@omnara/sdk'
import { fetchWebConfig, listAuthConnectors } from '@omnara/sdk/browser'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { queryClient } from './query'

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
  vi.spyOn(Math, 'random').mockReturnValue(0.5)
  vi.stubGlobal('location', new URL('https://app.example.test'))
})
afterEach(() => {
  queryClient.clear()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

it.each([true, false])(
  'lets the SDK own attempts for dashboard queries (recovery: %s)',
  async (recover) => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockImplementationOnce(() =>
        Promise.resolve(Response.json({ error: 'unavailable' }, { status: 503 })),
      )
      .mockImplementationOnce(() =>
        Promise.resolve(Response.json({ error: 'unavailable' }, { status: 503 })),
      )
      .mockImplementation(() =>
        Promise.resolve(
          Response.json(recover ? { agents: [] } : { error: 'unavailable' }, {
            status: recover ? 200 : 503,
          }),
        ),
      )
    const client = createOmnaraClient({ baseUrl: 'https://api.example.test/v1', fetch })
    const pending = queryClient.fetchQuery({
      queryKey: ['sdk-query'],
      queryFn: () => client.get({ url: '/agents' }),
    })
    const done = recover
      ? expect(pending).resolves.toMatchObject({ data: { agents: [] } })
      : expect(pending).rejects.toMatchObject({ status: 503 })
    await vi.runAllTimersAsync()
    await done
    expect(fetch).toHaveBeenCalledTimes(3)
  },
)

it.each([
  { name: 'auth connectors', read: listAuthConnectors, data: { connectors: [] }, expected: [] },
  {
    name: 'web configuration',
    read: fetchWebConfig,
    data: { api_url: 'https://api.example.test' },
    expected: { apiURL: 'https://api.example.test' },
  },
])('preserves retries for browser bootstrap: $name', async ({ name, read, data, expected }) => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockImplementationOnce(() =>
      Promise.resolve(Response.json({ error: 'unavailable' }, { status: 503 })),
    )
    .mockImplementationOnce(() =>
      Promise.resolve(Response.json({ error: 'unavailable' }, { status: 503 })),
    )
    .mockImplementation(() => Promise.resolve(Response.json(data)))
  vi.stubGlobal('fetch', fetch)
  const done = queryClient.fetchQuery({ queryKey: [name], queryFn: async () => read() })
  await vi.runAllTimersAsync()
  expect(await done).toMatchObject(expected)
  expect(fetch).toHaveBeenCalledTimes(3)
  const [request] = fetch.mock.calls[0] ?? []
  expect(request).toBeInstanceOf(Request)
  if (request instanceof Request) expect(request.credentials).toBe('include')
})
