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
  setSessionHistory,
  startSession,
  toolCallBlock,
  toolResultEvent,
  userInputEvent,
  waitForSnapshot,
} from './agent-chat-test-support'

const sdkMocks = chatSdkMocks()

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

    const stream = await connection(0)
    stream.push({
      event: 'agent_input',
      data: userInputEvent({ input_idempotency_key: sentIdempotencyKey() }),
    })
    const streaming = await waitForSnapshot(session, (s) => s.status === 'streaming')
    const userMessages = streaming.messages.filter((message) => message.role === 'user')
    expect(userMessages).toHaveLength(1)
    expect(userMessages[0]?.id).toBe('input-event')
    session.disconnect()
  })

  it('clears an optimistic input once the server accepts it', async () => {
    const queryClient = new QueryClient()
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries').mockResolvedValue()
    const session = startSession([], client(), queryClient)
    const stream = await connection(0)

    await session.sendMessage({ text: 'Hello' })
    const accepted = read(session)
    expect(accepted.status).toBe('ready')
    expect(accepted.messages.some((message) => message.id.startsWith('local:'))).toBe(false)
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: [expect.objectContaining({ _id: 'listQueuedBacklogInputs' })],
    })

    stream.push({
      event: 'agent_input',
      data: userInputEvent({ sequence: 12, input_idempotency_key: sentIdempotencyKey() }),
    })
    const durable = await waitForSnapshot(
      session,
      (state) => state.messages.at(-1)?.id === 'input-event',
    )
    expect(durable.messages.filter((message) => message.role === 'user')).toHaveLength(1)
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
    expect(teammateLoaded.messages.some((message) => message.id.startsWith('local:'))).toBe(false)
    expect(teammateLoaded.messages.filter((message) => message.role === 'user')).toHaveLength(1)

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
    let resolveSend!: (value: unknown) => void
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

    resolveSend({ data: { agent_input: { id: 'input-1' } } })
    await send
    const settled = read(session)
    expect(settled.messages.some((message) => message.id.startsWith('local:'))).toBe(false)
    expect(settled.messages.filter((message) => message.role === 'user')).toHaveLength(1)
    session.disconnect()
  })

  it('clears a failed send and its error when the durable echo proves the input landed', async () => {
    sdkMocks.createAgentInput.mockRejectedValue(new Error('response lost'))
    const queryClient = new QueryClient()
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries').mockResolvedValue()
    const session = startSession([], client(), queryClient)
    await connection(0)
    const stream = await connection(0)

    await expect(session.sendMessage({ text: 'Hello' })).rejects.toThrow('response lost')
    expect(read(session).status).toBe('error')
    expect(invalidate).not.toHaveBeenCalled()

    stream.push({
      event: 'agent_input',
      data: userInputEvent({ input_idempotency_key: sentIdempotencyKey() }),
    })
    const recovered = await waitForSnapshot(session, (state) =>
      state.messages.some((m) => m.id === 'input-event'),
    )
    expect(recovered.error).toBeUndefined()
    expect(recovered.status).not.toBe('error')
    expect(recovered.messages.filter((message) => message.role === 'user')).toHaveLength(1)
    expect(invalidate).toHaveBeenCalledWith(
      expect.objectContaining({ predicate: expect.any(Function) as unknown }),
    )
    session.disconnect()
  })

  it('resolves a send whose echo landed before its response failed, without an error', async () => {
    let rejectSend!: (reason: unknown) => void
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

  it('clears the lingering optimistic copy when an idempotent resend finds its echo already loaded', async () => {
    sdkMocks.createAgentInput.mockRejectedValueOnce(new Error('response lost'))
    const queryClient = new QueryClient()
    const session = startSession([], client(), queryClient)
    await connection(0)

    await expect(session.sendMessage({ text: 'Hello' })).rejects.toThrow('response lost')
    expect(read(session).status).toBe('error')

    const echo = userInputEvent({ input_idempotency_key: sentIdempotencyKey() })
    setSessionHistory(session, [echo])
    queryClient.setQueryData(['agent-chat-history', scope.orgID, scope.projectID, scope.agentID], {
      pages: [{ data: [echo] }],
      pageParams: [0],
    })

    await session.sendMessage({ text: 'Hello' })
    expect(sentIdempotencyKey(1)).toBe(sentIdempotencyKey(0))
    const settled = read(session)
    expect(settled.error).toBeUndefined()
    expect(settled.messages.some((message) => message.id.startsWith('local:'))).toBe(false)
    expect(settled.messages.filter((message) => message.role === 'user')).toHaveLength(1)
    session.disconnect()
  })

  it('restores the composer error state when the send fails', async () => {
    sdkMocks.createAgentInput.mockRejectedValue(new Error('input rejected'))
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
