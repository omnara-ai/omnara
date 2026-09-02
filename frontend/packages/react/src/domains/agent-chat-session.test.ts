import { AgentEventStreamError, ApiError } from '@omnara/sdk'
import { QueryClient } from '@tanstack/react-query'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import {
  chatSdkMocks,
  client,
  connection,
  createChatTestSession,
  event,
  messageText,
  read,
  resetChatTestHarness,
  scope,
  sentIdempotencyKey,
  startSession,
  toolCallBlock,
  toolResultEvent,
  userInputEvent,
  waitForSnapshot,
} from './agent-chat-test-support'
import { agentInputBacklogQueryKey } from './agent-input-backlog'

const sdkMocks = chatSdkMocks()

function acceptedInput(id: string, text: string) {
  return {
    data: {
      agent_input: {
        id,
        agent_id: 'agent',
        state: 'received',
        delivery_mode: 'queued' as const,
        input_kind: 'content' as const,
        content_blocks: [{ type: 'text' as const, text }],
        queued_at: '2026-07-14T00:00:00Z',
      },
    },
  }
}

describe('AgentChatSession input lifecycle', () => {
  beforeEach(resetChatTestHarness)

  it('waits for the history cursor before opening the event stream', async () => {
    const session = createChatTestSession()
    session.subscribe(() => undefined)

    await Promise.resolve()
    expect(sdkMocks.openAgentEventStream).not.toHaveBeenCalled()

    session.start(42)
    await connection(0)
    expect(sdkMocks.openAgentEventStream).toHaveBeenCalledWith(
      expect.objectContaining({ query: { after_sequence: 42, stream_deltas: true } }),
    )
    session.disconnect()
  })

  it('hydrates the event log into messages and reports an idle turn as ready', async () => {
    const session = startSession([
      userInputEvent(),
      event({ sequence: 12, content_blocks: [{ type: 'text', text: 'Hi there' }] }),
    ])
    await connection(0)
    const snapshot = read(session)

    expect(snapshot.status).toBe('ready')
    expect(snapshot.messages).toHaveLength(2)
    expect(snapshot.messages[0]).toMatchObject({ role: 'user' })
    expect(snapshot.messages[1]).toMatchObject({ id: 'turn:turn', role: 'assistant' })
    expect(messageText(snapshot.messages[1])).toEqual(['Hi there'])
    session.disconnect()
  })

  it('keeps consuming the stream after a tool call update', async () => {
    const session = startSession()
    const stream = await connection(0)

    stream.push({
      event: 'tool_call_update',
      data: { tool_call_id: 'tcl_123', state: 'ready' },
    })
    stream.push({ event: 'agent_input', data: userInputEvent() })

    const snapshot = await waitForSnapshot(
      session,
      (state) => state.messages.at(-1)?.id === 'input-event',
    )
    expect(snapshot.error).toBeUndefined()
    session.disconnect()
  })

  it('reports an in-flight turn as streaming after hydration', async () => {
    const session = startSession([
      userInputEvent(),
      event({
        sequence: 12,
        content_blocks: [toolCallBlock()],
      }),
    ])
    await connection(0)
    const snapshot = read(session)
    expect(snapshot.status).toBe('streaming')

    const stream = await connection(0)
    stream.push({
      event: 'tool_result',
      data: toolResultEvent({
        sequence: 13,
        tool_call_id: 'call',
        content_blocks: [{ type: 'text', text: '/workspace' }],
      }),
    })
    stream.push({
      event: 'model_output',
      data: event({ sequence: 14, content_blocks: [{ type: 'text', text: 'Done' }] }),
    })
    const finished = await waitForSnapshot(session, (s) => s.status === 'ready')

    const assistant = finished.messages.at(-1)
    expect(
      assistant?.parts.find((part) => part.type === 'dynamic-tool' && part.toolCallId === 'call'),
    ).toMatchObject({ state: 'output-available' })
    expect(messageText(assistant)).toEqual(['Done'])
    session.disconnect()
  })

  it('sends a message with an idempotency key and swaps the optimistic copy for the durable event', async () => {
    const session = startSession()
    await connection(0)

    const send = session.sendMessage({ text: 'Hello' })
    const submitted = read(session)
    expect(submitted.status).toBe('submitted')
    expect(submitted.messages.at(-1)).toMatchObject({ role: 'user' })
    expect(messageText(submitted.messages.at(-1))).toEqual(['Hello'])
    await send

    expect(sdkMocks.createAgentInput).toHaveBeenCalledWith(
      expect.objectContaining({
        path: scope,
        headers: { 'Idempotency-Key': expect.any(String) as unknown as string },
        body: {
          content_blocks: [
            {
              type: 'text',
              text: 'This message came from the Omnara web app. Reply with normal assistant text unless explicitly asked to message an integration.',
              metadata: { omnara_hidden: 'true' },
            },
            { type: 'text', text: 'Hello' },
          ],
        },
      }),
    )
    const accepted = read(session)
    expect(accepted.messages.some((message) => message.id.startsWith('local:'))).toBe(true)
    expect(session.getData().localInputs).toMatchObject([{ agentInputID: 'input-1' }])

    const stream = await connection(0)
    stream.push({
      event: 'agent_input',
      data: userInputEvent({ input_idempotency_key: sentIdempotencyKey() }),
    })
    const streaming = await waitForSnapshot(session, (state) =>
      state.messages.some((message) => message.id === 'input-event'),
    )
    const userMessages = streaming.messages.filter((message) => message.role === 'user')
    expect(userMessages).toHaveLength(1)
    expect(userMessages[0]?.id).toBe('input-event')
    session.disconnect()
  })

  it('keeps accepted conversation attachments visible until their durable echo', async () => {
    const session = startSession()
    await connection(0)
    const attachment = {
      data: 'aGk=',
      mediaType: 'text/plain' as const,
      filename: 'notes.txt',
      sizeBytes: 2,
    }

    const send = session.sendMessage({ text: '', attachments: [attachment] })

    expect(read(session).messages.at(-1)?.parts).toMatchObject([
      { type: 'data-media', data: attachment },
    ])
    await send
    expect(sdkMocks.createAgentInput).toHaveBeenCalledWith(
      expect.objectContaining({
        body: {
          content_blocks: [
            {
              type: 'text',
              text: 'This message came from the Omnara web app. Reply with normal assistant text unless explicitly asked to message an integration.',
              metadata: { omnara_hidden: 'true' },
            },
            {
              type: 'media',
              media_type: 'text/plain',
              filename: 'notes.txt',
              data: 'aGk=',
            },
          ],
        },
      }),
    )
    expect(session.getData().localInputs[0]?.attachments).toEqual([attachment])
    expect(read(session).messages.at(-1)?.parts).toMatchObject([
      { type: 'data-media', data: attachment },
    ])
    session.disconnect()
  })

  it('retains accepted immediate attachments until their durable echo arrives', async () => {
    sdkMocks.createAgentInput.mockResolvedValueOnce({
      data: {
        agent_input: {
          ...acceptedInput('input-1', '').data.agent_input,
          delivery_mode: 'immediate',
        },
      },
    })
    const session = startSession()
    await connection(0)
    const attachment = {
      data: 'aGk=',
      mediaType: 'text/plain' as const,
      filename: 'notes.txt',
      sizeBytes: 2,
    }

    await session.sendMessage({ text: '', attachments: [attachment] })

    expect(session.getData().localInputs[0]?.attachments).toEqual([attachment])
    session.disconnect()
  })

  it('keeps overlapping sends visible through reversed responses and events', async () => {
    let resolveFirst!: (value: ReturnType<typeof acceptedInput>) => void
    let resolveSecond!: (value: ReturnType<typeof acceptedInput>) => void
    sdkMocks.createAgentInput
      .mockImplementationOnce(() => new Promise((resolve) => (resolveFirst = resolve)))
      .mockImplementationOnce(() => new Promise((resolve) => (resolveSecond = resolve)))
    const session = startSession()
    const stream = await connection(0)

    const firstSend = session.sendMessage({ text: 'First' })
    const firstKey = sentIdempotencyKey(0)
    const secondSend = session.sendMessage({ text: 'Second' })
    const secondKey = sentIdempotencyKey(1)
    expect(read(session).messages.flatMap(messageText)).toEqual(['First', 'Second'])

    resolveSecond(acceptedInput('input-2', 'Second'))
    await secondSend
    expect(read(session).messages.flatMap(messageText)).toEqual(['First', 'Second'])

    stream.push({
      event: 'agent_input',
      data: userInputEvent({
        id: 'second-event',
        sequence: 12,
        agent_input_id: 'input-2',
        input_idempotency_key: secondKey,
        content_blocks: [{ type: 'text', text: 'Second' }],
      }),
    })
    await waitForSnapshot(session, (state) =>
      state.messages.some((message) => message.id === 'second-event'),
    )
    expect(read(session).messages.flatMap(messageText)).toEqual(['Second', 'First'])

    resolveFirst(acceptedInput('input-1', 'First'))
    await firstSend
    stream.push({
      event: 'agent_input',
      data: userInputEvent({
        id: 'first-event',
        sequence: 13,
        agent_input_id: 'input-1',
        input_idempotency_key: firstKey,
        content_blocks: [{ type: 'text', text: 'First' }],
      }),
    })
    const settled = await waitForSnapshot(session, (state) =>
      state.messages.some((message) => message.id === 'first-event'),
    )
    expect(settled.messages.filter((message) => message.role === 'user')).toHaveLength(2)
    expect(settled.messages.some((message) => message.id.startsWith('local:'))).toBe(false)
    session.disconnect()
  })

  it('revalidates a seeded backlog cache after a send is accepted', async () => {
    const queryClient = new QueryClient()
    const sessionClient = client()
    const queryKey = agentInputBacklogQueryKey(sessionClient, scope)
    queryClient.setQueryData(queryKey, { data: [], next_cursor: null })
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries')
    const session = startSession([], sessionClient, queryClient)
    await connection(0)

    await session.sendMessage({ text: 'Hello' }, 'backlog')

    expect(queryClient.getQueryData<{ data: { id: string }[] }>(queryKey)?.data).toMatchObject([
      { id: 'input-1' },
    ])
    expect(invalidate).toHaveBeenCalledWith({ queryKey })
    session.disconnect()
  })

  it('does not clear another pending send when one request fails', async () => {
    let rejectFirst!: (reason: Error) => void
    let resolveSecond!: (value: ReturnType<typeof acceptedInput>) => void
    sdkMocks.createAgentInput
      .mockImplementationOnce(() => new Promise((_resolve, reject) => (rejectFirst = reject)))
      .mockImplementationOnce(() => new Promise((resolve) => (resolveSecond = resolve)))
    const session = startSession()
    const stream = await connection(0)

    const firstSend = session.sendMessage({ text: 'First' })
    const secondSend = session.sendMessage({ text: 'Second' })
    const secondKey = sentIdempotencyKey(1)
    rejectFirst(new ApiError(422, 'first failed'))
    await expect(firstSend).rejects.toThrow('first failed')
    expect(read(session).messages.flatMap(messageText)).toEqual(['Second'])

    resolveSecond(acceptedInput('input-2', 'Second'))
    await secondSend
    expect(read(session).messages.flatMap(messageText)).toEqual(['Second'])
    stream.push({
      event: 'agent_input',
      data: userInputEvent({
        id: 'second-event',
        sequence: 12,
        agent_input_id: 'input-2',
        input_idempotency_key: secondKey,
        content_blocks: [{ type: 'text', text: 'Second' }],
      }),
    })
    const settled = await waitForSnapshot(session, (state) =>
      state.messages.some((message) => message.id === 'second-event'),
    )
    expect(settled.messages.filter((message) => message.role === 'user')).toHaveLength(1)
    expect(settled.messages.some((message) => message.id.startsWith('local:'))).toBe(false)
    session.disconnect()
  })

  it('keeps teammate and own durable inputs distinct after a send is accepted', async () => {
    const session = startSession()
    await connection(0)
    const stream = await connection(0)

    await session.sendMessage({ text: 'Hello' })

    stream.push({
      event: 'agent_input',
      data: userInputEvent({
        id: 'teammate-event',
        sequence: 12,
        agent_input_id: 'teammate-input',
        input_idempotency_key: 'teammate-key',
        content_blocks: [{ type: 'text', text: 'Hello' }],
      }),
    })
    await waitForSnapshot(session, (state) => state.messages.some((m) => m.id === 'teammate-event'))
    const teammateLoaded = read(session)
    expect(teammateLoaded.messages.some((message) => message.id.startsWith('local:'))).toBe(true)
    expect(teammateLoaded.messages.filter((message) => message.role === 'user')).toHaveLength(2)

    stream.push({
      event: 'agent_input',
      data: userInputEvent({
        id: 'own-event',
        sequence: 13,
        input_idempotency_key: sentIdempotencyKey(),
      }),
    })
    const durable = await waitForSnapshot(session, (state) =>
      state.messages.some((message) => message.id === 'own-event'),
    )
    expect(durable.messages.some((message) => message.id === 'own-event')).toBe(true)
    expect(durable.messages.some((message) => message.id.startsWith('local:'))).toBe(false)
    expect(durable.messages.filter((message) => message.role === 'user')).toHaveLength(2)
    session.disconnect()
  })

  it('clears the pending send the moment its durable event outraces the send response', async () => {
    let resolveSend!: (value: ReturnType<typeof acceptedInput>) => void
    sdkMocks.createAgentInput.mockImplementation(
      () => new Promise((resolve) => (resolveSend = resolve)),
    )
    const session = startSession()
    await connection(0)
    const stream = await connection(0)

    const send = session.sendMessage({ text: 'Hello' })
    stream.push({
      event: 'agent_input',
      data: userInputEvent({ input_idempotency_key: sentIdempotencyKey() }),
    })
    const raced = await waitForSnapshot(session, (state) =>
      state.messages.some((m) => m.id === 'input-event'),
    )
    expect(raced.messages.some((message) => message.id.startsWith('local:'))).toBe(false)
    expect(raced.messages.filter((message) => message.role === 'user')).toHaveLength(1)

    resolveSend(acceptedInput('input-1', 'Hello'))
    await send
    const settled = read(session)
    expect(settled.messages.some((message) => message.id.startsWith('local:'))).toBe(false)
    expect(settled.messages.filter((message) => message.role === 'user')).toHaveLength(1)
    session.disconnect()
  })

  it.each([
    ['network error', new Error('response lost')],
    ['request timeout', new ApiError(408, 'request timed out')],
    ['rate limit', new ApiError(429, 'rate limited')],
    ['server error', new ApiError(503, 'service unavailable')],
  ])(
    'restores a %s after the confirmation grace and reuses its idempotency key',
    async (_name, error) => {
      vi.useFakeTimers()
      sdkMocks.createAgentInput.mockRejectedValueOnce(error)
      const queryClient = new QueryClient()
      const invalidate = vi.spyOn(queryClient, 'invalidateQueries').mockResolvedValue()
      const session = startSession([], client(), queryClient)
      await connection(0)

      try {
        const send = session.sendMessage({ text: 'Hello' })
        const rejected = expect(send).rejects.toThrow()
        await vi.advanceTimersByTimeAsync(1)
        await rejected
        expect(read(session).messages).toEqual([])
        expect(read(session).error).toBeDefined()
        expect(invalidate).toHaveBeenCalledWith({
          queryKey: [expect.objectContaining({ _id: 'listQueuedBacklogInputs' })],
        })

        await session.sendMessage({ text: 'Hello' })
        expect(sdkMocks.createAgentInput).toHaveBeenCalledTimes(2)
        expect(sentIdempotencyKey(1)).toBe(sentIdempotencyKey(0))
      } finally {
        session.disconnect()
        vi.useRealTimers()
      }
    },
  )

  it('uses backlog confirmation when the event stream fails during the grace period', async () => {
    vi.useFakeTimers()
    sdkMocks.createAgentInput.mockRejectedValueOnce(new Error('response lost'))
    const queryClient = new QueryClient()
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries').mockResolvedValue()
    const session = startSession([], client(), queryClient)
    const stream = await connection(0)

    try {
      const send = session.sendMessage({ text: 'Hello' })
      const settled = expect(send).resolves.toBeUndefined()
      const key = sentIdempotencyKey()
      await vi.advanceTimersByTimeAsync(0)
      expect(invalidate).toHaveBeenCalled()

      stream.fail(
        new AgentEventStreamError({
          kind: 'api',
          code: 'internal_error',
          message: 'event projection failed',
        }),
      )
      await vi.advanceTimersByTimeAsync(0)
      expect(read(session).error?.message).toBe('event projection failed')

      session.confirmBacklogInputs([
        { ...acceptedInput('input-1', 'Hello').data.agent_input, input_idempotency_key: key },
      ])
      await vi.advanceTimersByTimeAsync(1)

      await settled
      expect(session.getData().localInputs).toMatchObject([{ agentInputID: 'input-1' }])
    } finally {
      session.disconnect()
      vi.useRealTimers()
    }
  })

  it('resolves a send whose echo landed before its response failed, without an error', async () => {
    let rejectSend!: (reason: Error) => void
    sdkMocks.createAgentInput.mockImplementation(
      () => new Promise((_resolve, reject) => (rejectSend = reject)),
    )
    const session = startSession()
    await connection(0)
    const stream = await connection(0)

    const send = session.sendMessage({ text: 'Hello' })
    stream.push({
      event: 'agent_input',
      data: userInputEvent({ input_idempotency_key: sentIdempotencyKey() }),
    })
    await waitForSnapshot(session, (state) => state.messages.some((m) => m.id === 'input-event'))

    rejectSend(new Error('response lost'))
    await expect(send).resolves.toBeUndefined()
    const settled = read(session)
    expect(settled.error).toBeUndefined()
    expect(settled.status).not.toBe('error')
    expect(settled.messages.filter((message) => message.role === 'user')).toHaveLength(1)
    session.disconnect()
  })

  it('clears a send error when its durable echo arrives after the confirmation grace', async () => {
    vi.useFakeTimers()
    sdkMocks.createAgentInput.mockRejectedValue(new Error('response lost'))
    const session = startSession()
    const stream = await connection(0)

    try {
      const send = session.sendMessage({ text: 'Hello' })
      const key = sentIdempotencyKey()
      const rejected = expect(send).rejects.toThrow('response lost')
      await vi.advanceTimersByTimeAsync(1)
      await rejected
      expect(read(session).status).toBe('error')

      stream.push({
        event: 'agent_input',
        data: userInputEvent({ input_idempotency_key: key }),
      })
      const recovered = await waitForSnapshot(session, (state) => state.error == null)

      expect(recovered.status).not.toBe('error')
      expect(recovered.messages.filter((message) => message.role === 'user')).toHaveLength(1)
    } finally {
      session.disconnect()
      vi.useRealTimers()
    }
  })

  it('uses backlog correlation to confirm a send whose response was lost', async () => {
    sdkMocks.createAgentInput.mockRejectedValue(new Error('response lost'))
    const session = startSession()
    await connection(0)
    const attachment = {
      data: 'aGk=',
      mediaType: 'text/plain' as const,
      filename: 'notes.txt',
      sizeBytes: 2,
    }

    const send = session.sendMessage({ text: 'Hello', attachments: [attachment] }, 'backlog')
    const key = sentIdempotencyKey()
    expect(session.getData().localInputs[0]?.attachments).toEqual([attachment])

    session.confirmBacklogInputs([
      {
        id: 'input-1',
        agent_id: 'agent',
        state: 'received',
        delivery_mode: 'queued',
        input_kind: 'content',
        input_idempotency_key: key,
        content_blocks: [{ type: 'text', text: 'Hello' }],
        queued_at: '2026-07-14T00:00:00Z',
      },
    ])
    await send
    expect(read(session).error).toBeUndefined()
    expect(session.getData().localInputs).toMatchObject([
      { agentInputID: 'input-1', attachmentCount: 1 },
    ])
    expect(session.getData().localInputs[0]?.attachments).toBeUndefined()
    session.disconnect()
  })

  it('optimistically dismisses local backlog inputs and restores them on rollback', async () => {
    const queryClient = new QueryClient()
    const sessionClient = client()
    const session = startSession([], sessionClient, queryClient)
    await connection(0)
    await session.sendMessage({ text: 'Hello' })

    const rollback = session.beginBacklogInputCancellation(['input-1'])
    expect(session.getData().localInputs).toEqual([])

    rollback()
    expect(session.getData().localInputs).toMatchObject([{ agentInputID: 'input-1' }])
    session.disconnect()
  })

  it('compares failed attachment retries by their wire payload', async () => {
    sdkMocks.createAgentInput
      .mockRejectedValueOnce(new Error('response lost'))
      .mockRejectedValueOnce(new Error('response lost'))
    const session = startSession()
    await connection(0)
    const first = {
      data: 'b25l',
      mediaType: 'text/plain' as const,
      filename: 'notes.txt',
      sizeBytes: 3,
    }
    const resized = { ...first, sizeBytes: 4 }
    const changed = { ...resized, data: 'dHdv' }

    await expect(session.sendMessage({ text: 'Review', attachments: [first] })).rejects.toThrow(
      'response lost',
    )
    await expect(session.sendMessage({ text: 'Review', attachments: [resized] })).rejects.toThrow(
      'response lost',
    )
    expect(sentIdempotencyKey(1)).toBe(sentIdempotencyKey(0))

    await session.sendMessage({ text: 'Review', attachments: [changed] })
    expect(sentIdempotencyKey(2)).not.toBe(sentIdempotencyKey(1))
    session.disconnect()
  })

  it('restores the composer error state when the send fails', async () => {
    sdkMocks.createAgentInput.mockRejectedValue(new ApiError(422, 'input rejected'))
    const session = startSession()
    await connection(0)

    await expect(session.sendMessage({ text: 'Hello' })).rejects.toThrow('input rejected')
    const snapshot = read(session)
    expect(snapshot.status).toBe('error')
    expect(snapshot.error?.message).toBe('input rejected')
    expect(snapshot.messages).toHaveLength(0)
    session.disconnect()
  })
})
