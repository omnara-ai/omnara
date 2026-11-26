import { AgentEventStreamError } from '@omnara/sdk'
import { beforeEach, describe, expect, it } from 'vitest'

import {
  chatTransport,
  connection,
  read,
  resetChatTestHarness,
  startSession,
  userInputEvent,
  waitForSnapshot,
} from './agent-chat-test-support'

const transport = chatTransport()

describe('AgentChatSession stream connection', () => {
  beforeEach(resetChatTestHarness)

  it('opens the continuous follower from the initial durable cursor', async () => {
    const session = startSession()
    await connection(0)
    expect(transport.openAgentEventStream).toHaveBeenCalledWith(
      expect.objectContaining({
        query: { after_sequence: 0, stream_deltas: true },
      }),
    )
    session.disconnect()
  })

  it('surfaces a fatal stream response', async () => {
    const session = startSession()
    const stream = await connection(0)
    stream.fail(
      new AgentEventStreamError({
        kind: 'http',
        message: 'Agent event stream request failed with HTTP 401',
        status: 401,
      }),
    )
    const snapshot = await waitForSnapshot(session, (s) => s.status === 'error')

    expect(snapshot.error?.message).toBe('Agent event stream request failed with HTTP 401')
    expect(transport.openAgentEventStream).toHaveBeenCalledTimes(1)
    session.disconnect()
  })

  it('reopens the stream from the last cursor on reconnect', async () => {
    const session = startSession()
    const stream = await connection(0)
    stream.fail(
      new AgentEventStreamError({
        kind: 'http',
        message: 'Agent event stream request failed with HTTP 401',
        status: 401,
      }),
    )
    await waitForSnapshot(session, (s) => s.status === 'error')

    expect(session.getData().streamError?.message).toBe(
      'Agent event stream request failed with HTTP 401',
    )
    session.reconnect()

    expect(read(session).error).toBeUndefined()
    expect(session.getData().streamError).toBeUndefined()
    const reopened = await connection(1)
    reopened.push({ event: 'agent_input', data: userInputEvent() })
    await waitForSnapshot(session, (s) => s.messages.length === 1)
    expect(transport.openAgentEventStream).toHaveBeenLastCalledWith(
      expect.objectContaining({ query: { after_sequence: 0, stream_deltas: true } }),
    )
    session.disconnect()
  })

  it('surfaces a terminal API stream error', async () => {
    const session = startSession()
    const stream = await connection(0)
    stream.fail(
      new AgentEventStreamError({
        kind: 'api',
        code: 'internal_error',
        message: 'event projection failed',
      }),
    )

    const snapshot = await waitForSnapshot(session, (state) => state.status === 'error')
    expect(snapshot.error?.message).toBe('event projection failed')
    expect(transport.openAgentEventStream).toHaveBeenCalledTimes(1)
    session.disconnect()
  })

  it('surfaces a stream contract violation without reconnecting', async () => {
    const session = startSession()
    const stream = await connection(0)
    stream.fail(
      new AgentEventStreamError({
        kind: 'contract',
        message: 'Agent event stream received data that does not match the API contract',
      }),
    )

    const snapshot = await waitForSnapshot(session, (state) => state.status === 'error')
    expect(snapshot.error?.message).toBe(
      'Agent event stream received data that does not match the API contract',
    )
    expect(transport.openAgentEventStream).toHaveBeenCalledTimes(1)
    session.disconnect()
  })
})
