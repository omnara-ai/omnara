import { describe, expect, it, vi } from 'vitest'

import { AgentEventStreamError, openAgentEventStream } from './agent-event-stream'
import { createOmnaraClient } from './client'

const path = { orgID: 'org', projectID: 'project', agentID: 'agent' }

function clientReturning(response: Response) {
  const fetch = vi.fn().mockResolvedValue(response)
  const client = createOmnaraClient({ baseUrl: 'https://api.example.test' })
  client.setConfig({ fetch })
  return { client, fetch }
}

async function readStream(response: Response) {
  const { client, fetch } = clientReturning(response)
  const { stream } = await openAgentEventStream({ client, path })
  const frames = []
  for await (const frame of stream) frames.push(frame)
  return { fetch, frames }
}

async function streamError(response: Response): Promise<AgentEventStreamError> {
  try {
    await readStream(response)
  } catch (error) {
    if (error instanceof AgentEventStreamError) return error
    throw new Error('stream returned an unexpected error', { cause: error })
  }
  throw new Error('stream unexpectedly completed')
}

describe('openAgentEventStream', () => {
  it('validates every frame and returns typed data', async () => {
    const result = await readStream(
      new Response('data: {"error":"stream ended","code":"internal_error"}\n\n', {
        status: 200,
        headers: { 'Content-Type': 'text/event-stream' },
      }),
    )

    expect(result.frames).toEqual([{ error: 'stream ended', code: 'internal_error' }])
    expect(result.fetch).toHaveBeenCalledTimes(1)
  })

  it.each(['{"event_kind":"unknown"}', 'not JSON'])(
    'stops after one malformed frame instead of reconnecting: %s',
    async (data) => {
      const { client, fetch } = clientReturning(
        new Response(`data: ${data}\n\n`, {
          status: 200,
          headers: { 'Content-Type': 'text/event-stream' },
        }),
      )
      const { stream } = await openAgentEventStream({ client, path })

      await expect(stream.next()).rejects.toMatchObject({
        kind: 'contract',
        retryable: false,
      })
      expect(fetch).toHaveBeenCalledTimes(1)
    },
  )

  it.each([
    { status: 401, retryable: false },
    { status: 408, retryable: true },
    { status: 429, retryable: true },
    { status: 503, retryable: true },
  ])('classifies HTTP $status responses', async ({ status, retryable }) => {
    const error = await streamError(new Response(null, { status }))

    expect(error).toMatchObject({
      kind: 'http',
      retryable,
      status,
    })
  })

  it('classifies network failures as retryable transport errors', async () => {
    const fetch = vi.fn().mockRejectedValue(new TypeError('connection reset'))
    const client = createOmnaraClient({ baseUrl: 'https://api.example.test' })
    client.setConfig({ fetch })
    const { stream } = await openAgentEventStream({ client, path })

    await expect(stream.next()).rejects.toMatchObject({
      kind: 'transport',
      retryable: true,
    })
    expect(fetch).toHaveBeenCalledTimes(1)
  })
})
