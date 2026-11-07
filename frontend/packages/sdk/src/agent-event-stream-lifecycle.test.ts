import { afterEach, expect, it, vi } from 'vitest'

import { trackAbortListeners } from './abort-test-support'
import { openAgentEventStream } from './agent-event-stream'
import { createOmnaraClient } from './client'

const path = { orgID: 'org', projectID: 'project', agentID: 'agent' }
const suffix = 'a'.repeat(26)
const frame = {
  org_id: `org_${suffix}`,
  project_id: `proj_${suffix}`,
  agent_id: `agt_${suffix}`,
  agent_profile_id: null,
  parent_agent_id: null,
  root_agent_id: `agt_${suffix}`,
  tool_call_id: `tcl_${suffix}`,
  state: 'running',
}
const bytes = new TextEncoder().encode(`data: ${JSON.stringify(frame)}\n\n`)

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

it('disposes caller links before reconnecting and after the final failure', async () => {
  vi.useFakeTimers()
  vi.spyOn(Math, 'random').mockReturnValue(0)
  const source = new AbortController()
  const listeners = trackAbortListeners(source.signal)
  let attempts = 0
  const client = createOmnaraClient({
    baseUrl: 'https://api.example.test',
    fetch: () =>
      Promise.resolve(
        ++attempts < 5
          ? new Response('', { headers: { 'Content-Type': 'text/event-stream' } })
          : new Response(null, { status: 401 }),
      ),
  })
  const next = openAgentEventStream({
    client,
    path,
    signal: source.signal,
    onConnectionStateChange: (state) => {
      expect(listeners.size).toBe(state.state === 'connected' ? 1 : 0)
    },
  }).next()
  const done = expect(next).rejects.toMatchObject({ kind: 'http', status: 401 })
  await vi.runAllTimersAsync()
  await done
  expect(attempts).toBe(5)
  expect(listeners.size).toBe(0)
})

it.each(['return', 'abort'] as const)(
  'disposes an active connection on consumer %s',
  async (finish) => {
    const source = new AbortController()
    const listeners = trackAbortListeners(source.signal)
    const cancel = vi.fn()
    let requestSignal: AbortSignal | undefined
    const client = createOmnaraClient({
      baseUrl: 'https://api.example.test',
      fetch: (request) => {
        requestSignal = new Request(request).signal
        return Promise.resolve(
          new Response(
            new ReadableStream({
              start(controller) {
                controller.enqueue(bytes)
              },
              cancel,
            }),
            { headers: { 'Content-Type': 'text/event-stream' } },
          ),
        )
      },
    })
    const stream = openAgentEventStream({ client, path, signal: source.signal })
    await expect(stream.next()).resolves.toMatchObject({ value: frame, done: false })
    expect(listeners.size).toBe(1)
    if (finish === 'return') await stream.return(undefined)
    else {
      const reason = { caller: 'stopped' }
      const next = stream.next()
      source.abort(reason)
      await expect(next).resolves.toEqual({ value: undefined, done: true })
      expect(requestSignal?.reason).toBe(reason)
    }
    expect(cancel).toHaveBeenCalledOnce()
    expect(listeners.size).toBe(0)
  },
)
