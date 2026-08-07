/* eslint-disable max-lines -- session scenarios intentionally share the same stream harness. */
import {
  type AgentEvent,
  AgentEventStreamError,
  type AgentInputEvent,
  type ModelOutputEvent,
  type ModelOutputStreamDelta,
  type OmnaraClient,
  type ToolResultEvent,
} from '@omnara/sdk'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const sdkMocks = vi.hoisted(() => {
  class MockAgentEventStreamError extends Error {
    readonly kind: 'aborted' | 'contract' | 'http' | 'transport'
    readonly retryable: boolean
    readonly status: number | undefined

    constructor({
      kind,
      message,
      retryable,
      status,
    }: {
      kind: 'aborted' | 'contract' | 'http' | 'transport'
      message: string
      retryable: boolean
      status?: number
    }) {
      super(message)
      this.kind = kind
      this.retryable = retryable
      this.status = status
    }
  }

  return {
    AgentEventStreamError: MockAgentEventStreamError,
    createAgentInput: vi.fn(),
    openAgentEventStream: vi.fn(),
  }
})

vi.mock('@omnara/sdk', () => ({
  AgentEventStreamError: sdkMocks.AgentEventStreamError,
  openAgentEventStream: sdkMocks.openAgentEventStream,
  sdk: {
    createAgentInput: sdkMocks.createAgentInput,
  },
}))

import { QueryClient } from '@tanstack/react-query'

import { AgentChatSession } from './agent-chat'
import {
  type AgentChatStatus,
  agentEventsToMessages,
  type ModelOutputDelta,
  type OmnaraUIMessage,
  projectAgentChat,
  sequenceNumber,
} from './agent-chat-messages'

const scope = { orgID: 'org', projectID: 'project', agentID: 'agent' }

function client(statuses: number[] = [200]): OmnaraClient {
  let connection = 0
  return {
    getConfig: () => ({
      fetch: () => {
        const status = statuses[connection] ?? statuses.at(-1) ?? 200
        connection += 1
        return Promise.resolve(
          new Response(': ok\n\n', {
            status,
            headers: { 'Content-Type': 'text/event-stream' },
          }),
        )
      },
    }),
  } as unknown as OmnaraClient
}

function event(overrides: Partial<ModelOutputEvent> = {}): ModelOutputEvent {
  return {
    id: 'event',
    org_id: 'org',
    project_id: 'project',
    agent_id: 'agent',
    turn_id: 'turn',
    turn_sequence: 2,
    is_opening_event: false,
    sequence: 11,
    event_kind: 'model_output',
    model_call_context_id: 'mcc',
    stop_reason: 'end_turn',
    content_blocks: [],
    created_at: '2026-07-14T00:00:00Z',
    ...overrides,
  }
}

function userInputEvent(overrides: Partial<AgentInputEvent> = {}): AgentInputEvent {
  return {
    id: 'input-event',
    org_id: 'org',
    project_id: 'project',
    agent_id: 'agent',
    turn_id: 'turn',
    turn_sequence: 2,
    is_opening_event: true,
    sequence: 11,
    event_kind: 'agent_input',
    agent_input_id: 'input-1',
    input_kind: 'content',
    content_blocks: [{ type: 'text', text: 'Hello' }],
    created_at: '2026-07-14T00:00:00Z',
    ...overrides,
  }
}

function toolResultEvent(overrides: Partial<ToolResultEvent> = {}): ToolResultEvent {
  return {
    id: 'result-event',
    org_id: 'org',
    project_id: 'project',
    agent_id: 'agent',
    turn_id: 'turn',
    turn_sequence: 2,
    is_opening_event: false,
    sequence: 12,
    event_kind: 'tool_result',
    tool_call_id: 'call',
    outcome: 'succeeded',
    content_blocks: [],
    created_at: '2026-07-14T00:00:00Z',
    ...overrides,
  }
}

function controlEvent(overrides: Partial<AgentInputEvent> = {}): AgentInputEvent {
  return userInputEvent({
    id: 'cancel-event',
    content_blocks: [],
    input_kind: 'control',
    control_type: 'cancel_current',
    ...overrides,
  })
}

function configChangeEvent(overrides: Partial<AgentInputEvent> = {}): AgentInputEvent {
  return userInputEvent({
    id: 'config-event',
    content_blocks: [],
    input_kind: 'config_change',
    ...overrides,
  })
}

function toolCallBlock(toolCallID = 'call') {
  const input = { command: 'pwd' }
  return {
    type: 'tool_call' as const,
    tool_call_id: toolCallID,
    tool_type: 'built_in' as const,
    name: 'shell',
    input,
  }
}

function delta(
  seq: number,
  deltaEvent: ModelOutputStreamDelta,
  overrides: Partial<ModelOutputDelta> = {},
): ModelOutputDelta {
  return {
    turn_id: 'turn',
    model_call_context_id: 'mcc',
    seq,
    source_seq_start: seq,
    source_seq_end: seq,
    event: deltaEvent,
    ...overrides,
  }
}

interface Frame {
  event: string
  data: unknown
}

class FakeStream {
  private queue: Frame[] = []
  private wake: (() => void) | null = null
  private ended = false
  private failure: Error | undefined

  push(frame: Frame): void {
    this.queue.push(frame)
    this.wake?.()
  }

  end(): void {
    this.ended = true
    this.wake?.()
  }

  fail(error: Error): void {
    this.failure = error
    this.end()
  }

  async *drive(open: () => Promise<boolean>): AsyncGenerator {
    if (!(await open())) return
    for (;;) {
      while (this.queue.length > 0) {
        const frame = this.queue.shift()
        if (frame != null) yield frame.data
      }
      if (this.ended) {
        if (this.failure != null) throw this.failure
        return
      }
      await new Promise<void>((resolve) => {
        this.wake = resolve
      })
      this.wake = null
    }
  }
}

const connections: FakeStream[] = []

function installStreaming(): void {
  sdkMocks.openAgentEventStream.mockImplementation(async (options: Record<string, unknown>) => {
    const stream = new FakeStream()
    connections.push(stream)
    await Promise.resolve()
    const signal = options.signal as AbortSignal | undefined
    signal?.addEventListener('abort', () => {
      stream.end()
    })
    const open = async () => {
      const configuredFetch = (options.client as OmnaraClient).getConfig().fetch ?? fetch
      const response = await configuredFetch(new Request('https://example.test/events/stream'))
      if (!response.ok) {
        const status = response.status
        throw new AgentEventStreamError({
          kind: 'http',
          message: `Agent event stream request failed with HTTP ${status}`,
          retryable: status === 408 || status === 429 || status >= 500,
          status,
        })
      }
      return true
    }
    return { stream: stream.drive(open) }
  })
}

async function connection(index: number): Promise<FakeStream> {
  await vi.waitFor(() => {
    expect(connections.length).toBeGreaterThan(index)
  })
  const stream = connections[index]
  if (stream == null) throw new Error(`missing connection ${index}`)
  return stream
}

interface ChatState {
  messages: OmnaraUIMessage[]
  status: AgentChatStatus
  error: Error | undefined
}

const sessionHistory = new WeakMap<AgentChatSession, AgentEvent[]>()
const sessionHasOlderEvents = new WeakMap<AgentChatSession, boolean>()

function read(session: AgentChatSession): ChatState {
  const sessionData = session.getData()
  const data = {
    ...sessionData,
    events: [...(sessionHistory.get(session) ?? []), ...sessionData.events],
    hasOlderEvents: sessionHasOlderEvents.get(session) ?? false,
  }
  const { messages, status } = projectAgentChat(data)
  return { messages, status, error: data.error }
}

function startSession(
  history: AgentEvent[] = [],
  sessionClient = client(),
  queryClient = new QueryClient(),
  hasOlderEvents = false,
): AgentChatSession {
  const session = new AgentChatSession({
    client: sessionClient,
    queryClient,
    ...scope,
    reconnectDelayMs: 1,
  })
  sessionHistory.set(session, history)
  sessionHasOlderEvents.set(session, hasOlderEvents)
  session.start(sequenceNumber(history.at(-1)?.sequence))
  session.subscribe(() => undefined)
  return session
}

async function waitForSnapshot(
  session: AgentChatSession,
  predicate: (state: ChatState) => boolean,
): Promise<ChatState> {
  await vi.waitFor(() => {
    expect(predicate(read(session))).toBe(true)
  })
  return read(session)
}

function messageText(message: OmnaraUIMessage | undefined): string[] {
  return message?.parts.flatMap((part) => (part.type === 'text' ? [part.text] : [])) ?? []
}

function sentIdempotencyKey(call = 0): string {
  const args = sdkMocks.createAgentInput.mock.calls[call]?.[0] as
    | { headers?: { 'Idempotency-Key'?: string } }
    | undefined
  const key = args?.headers?.['Idempotency-Key']
  if (key == null) throw new Error(`no createAgentInput call ${call} recorded`)
  return key
}

describe('AgentChatSession', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    connections.length = 0
    installStreaming()
    sdkMocks.createAgentInput.mockResolvedValue({ data: { agent_input: { id: 'input-1' } } })
  })

  it('waits for the history cursor before opening the event stream', async () => {
    const session = new AgentChatSession({
      client: client(),
      queryClient: new QueryClient(),
      ...scope,
      reconnectDelayMs: 1,
    })
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
              metadata: { omnara_hidden: true },
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

  it('keeps an optimistic input while cancellation races its durable event', async () => {
    const queryClient = new QueryClient()
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries').mockResolvedValue()
    const session = startSession([], client(), queryClient)
    const stream = await connection(0)

    await session.sendMessage({ text: 'Hello' })
    stream.push({
      event: 'agent_input',
      data: controlEvent({ sequence: 12 }),
    })
    await vi.waitFor(() => {
      expect(invalidate).toHaveBeenCalledTimes(1)
    })

    const cancelled = read(session)
    expect(cancelled.status).toBe('submitted')
    expect(cancelled.messages.at(-1)?.id).toMatch(/^local:/)
    expect(messageText(cancelled.messages.at(-1))).toEqual(['Hello'])

    stream.push({
      event: 'agent_input',
      data: userInputEvent({ sequence: 13, input_idempotency_key: sentIdempotencyKey() }),
    })
    const durable = await waitForSnapshot(
      session,
      (state) => state.messages.at(-1)?.id === 'input-event',
    )
    expect(durable.messages.filter((message) => message.role === 'user')).toHaveLength(1)
    session.disconnect()
  })

  it('keeps a pending send until its own durable event lands, not any teammate message', async () => {
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
    const stillPending = read(session)
    expect(stillPending.status).toBe('submitted')
    expect(stillPending.messages.some((message) => message.id.startsWith('local:'))).toBe(true)

    stream.push({
      event: 'agent_input',
      data: userInputEvent({
        id: 'own-event',
        sequence: 13,
        input_idempotency_key: sentIdempotencyKey(),
      }),
    })
    const durable = await waitForSnapshot(session, (state) => state.status === 'streaming')
    expect(durable.messages.some((message) => message.id === 'own-event')).toBe(true)
    expect(durable.messages.some((message) => message.id.startsWith('local:'))).toBe(false)
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
    sessionHistory.set(session, [echo])
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

  it('streams accepted deltas as a live preview and swaps to the durable output', async () => {
    const session = startSession()
    await connection(0)
    const stream = await connection(0)

    stream.push({ event: 'agent_input', data: userInputEvent() })
    stream.push({
      event: 'model_output_delta',
      data: delta(1, { kind: 'block_start', block_index: 0, block: { kind: 'text' } }),
    })
    stream.push({
      event: 'model_output_delta',
      data: delta(2, { kind: 'text_delta', block_index: 0, delta: 'Hello ' }),
    })
    stream.push({
      event: 'model_output_delta',
      data: delta(3, { kind: 'text_delta', block_index: 0, delta: 'world' }),
    })
    const streaming = await waitForSnapshot(
      session,
      (s) => messageText(s.messages.at(-1))[0] === 'Hello world',
    )
    expect(streaming.status).toBe('streaming')
    expect(streaming.messages.at(-1)?.id).toBe('turn:turn')

    stream.push({
      event: 'model_output',
      data: event({
        model_call_context_id: 'mcc',
        sequence: 12,
        content_blocks: [{ type: 'text', text: 'Hello world' }],
      }),
    })
    const finished = await waitForSnapshot(session, (s) => s.status === 'ready')
    expect(messageText(finished.messages.at(-1))).toEqual(['Hello world'])
    session.disconnect()
  })

  it('clears a model stream error when a durable event confirms recovery', async () => {
    const session = startSession()
    const stream = await connection(0)

    stream.push({
      event: 'model_output_delta',
      data: delta(
        1,
        { kind: 'error', error: { message: 'model call failed' } },
        { model_call_context_id: 'failed-call' },
      ),
    })
    const failed = await waitForSnapshot(session, (state) => state.status === 'error')
    expect(failed.error?.message).toBe('model call failed')

    stream.push({
      event: 'model_output',
      data: event({
        model_call_context_id: 'recovered-call',
        content_blocks: [{ type: 'text', text: 'Recovered' }],
      }),
    })
    const recovered = await waitForSnapshot(session, (state) => state.status === 'ready')
    expect(recovered.error).toBeUndefined()
    expect(messageText(recovered.messages.at(-1))).toEqual(['Recovered'])
    session.disconnect()
  })

  it('ignores deltas from a stream joined mid-call and renders the durable output', async () => {
    const session = startSession()
    await connection(0)
    const stream = await connection(0)

    stream.push({ event: 'agent_input', data: userInputEvent() })
    stream.push({
      event: 'model_output_delta',
      data: delta(5, { kind: 'text_delta', block_index: 0, delta: 'world' }),
    })
    stream.push({
      event: 'model_output',
      data: event({
        model_call_context_id: 'mcc',
        sequence: 12,
        content_blocks: [{ type: 'text', text: 'Hello world' }],
      }),
    })
    const finished = await waitForSnapshot(
      session,
      (s) => s.status === 'ready' && messageText(s.messages.at(-1))[0] === 'Hello world',
    )

    expect(messageText(finished.messages.at(-1))).toEqual(['Hello world'])
    session.disconnect()
  })

  it('drops late deltas after the durable output completed the call', async () => {
    const session = startSession()
    await connection(0)
    const stream = await connection(0)

    stream.push({ event: 'agent_input', data: userInputEvent() })
    stream.push({
      event: 'model_output',
      data: event({
        model_call_context_id: 'mcc',
        sequence: 12,
        content_blocks: [toolCallBlock()],
      }),
    })
    stream.push({
      event: 'model_output_delta',
      data: delta(1, { kind: 'text_delta', block_index: 0, delta: 'stale' }),
    })
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
    const finished = await waitForSnapshot(
      session,
      (s) => s.status === 'ready' && messageText(s.messages.at(-1)).includes('Done'),
    )

    expect(messageText(finished.messages.at(-1))).toEqual(['Done'])
    session.disconnect()
  })

  it('streams a tool call under its shared public id and applies the result', async () => {
    const session = startSession()
    await connection(0)
    const stream = await connection(0)

    stream.push({ event: 'agent_input', data: userInputEvent() })
    stream.push({
      event: 'model_output_delta',
      data: delta(1, {
        kind: 'block_start',
        block_index: 0,
        block: { kind: 'tool_use', tool_call_id: 'public-call', tool_name: 'shell' },
      }),
    })
    stream.push({
      event: 'model_output_delta',
      data: delta(2, { kind: 'tool_arguments_delta', block_index: 0, delta: '{"command":"pwd"}' }),
    })
    const streaming = await waitForSnapshot(
      session,
      (s) =>
        s.messages
          .at(-1)
          ?.parts.some(
            (part) => part.type === 'dynamic-tool' && part.state === 'input-streaming',
          ) === true,
    )
    expect(streaming.messages.at(-1)?.parts.at(-1)).toMatchObject({
      type: 'dynamic-tool',
      toolCallId: 'public-call',
      toolName: 'shell',
      input: { command: 'pwd' },
    })

    stream.push({
      event: 'model_output',
      data: event({
        model_call_context_id: 'mcc',
        sequence: 12,
        content_blocks: [toolCallBlock('public-call')],
      }),
    })
    stream.push({
      event: 'tool_result',
      data: toolResultEvent({
        sequence: 13,
        tool_call_id: 'public-call',
        content_blocks: [{ type: 'text', text: '/workspace' }],
      }),
    })
    stream.push({
      event: 'model_output',
      data: event({ sequence: 14, content_blocks: [{ type: 'text', text: 'Done' }] }),
    })
    const finished = await waitForSnapshot(session, (s) => s.status === 'ready')

    const toolParts = finished.messages.at(-1)?.parts.filter((part) => part.type === 'dynamic-tool')
    expect(toolParts).toHaveLength(1)
    expect(toolParts?.[0]).toMatchObject({
      toolCallId: 'public-call',
      state: 'output-available',
      output: { contentBlocks: [{ type: 'text', text: '/workspace' }] },
    })
    session.disconnect()
  })

  it('shows an ephemeral thinking placeholder for an empty thinking block', async () => {
    const session = startSession()
    await connection(0)
    const stream = await connection(0)

    stream.push({ event: 'agent_input', data: userInputEvent() })
    stream.push({
      event: 'model_output_delta',
      data: delta(1, { kind: 'block_start', block_index: 0, block: { kind: 'thinking' } }),
    })
    const thinking = await waitForSnapshot(
      session,
      (s) =>
        s.messages
          .at(-1)
          ?.parts.some((part) => part.type === 'data-thinking' && part.data.active) === true,
    )
    expect(thinking.status).toBe('streaming')

    stream.push({
      event: 'model_output_delta',
      data: delta(2, { kind: 'block_stop', block_index: 0 }),
    })
    stream.push({
      event: 'model_output',
      data: event({
        model_call_context_id: 'mcc',
        sequence: 12,
        content_blocks: [{ type: 'text', text: 'Done' }],
      }),
    })
    const finished = await waitForSnapshot(session, (s) => s.status === 'ready')

    const assistant = finished.messages.at(-1)
    expect(assistant?.parts.some((part) => part.type === 'data-thinking')).toBe(false)
    expect(assistant?.parts.some((part) => part.type === 'reasoning')).toBe(false)
    expect(messageText(assistant)).toEqual(['Done'])
    session.disconnect()
  })

  it('replaces the thinking placeholder with streamed reasoning text', async () => {
    const session = startSession()
    await connection(0)
    const stream = await connection(0)

    stream.push({ event: 'agent_input', data: userInputEvent() })
    stream.push({
      event: 'model_output_delta',
      data: delta(1, { kind: 'block_start', block_index: 0, block: { kind: 'thinking' } }),
    })
    stream.push({
      event: 'model_output_delta',
      data: delta(2, { kind: 'thinking_delta', block_index: 0, delta: 'Planning' }),
    })
    const snapshot = await waitForSnapshot(
      session,
      (s) => s.messages.at(-1)?.parts.some((part) => part.type === 'reasoning') === true,
    )

    const assistant = snapshot.messages.at(-1)
    expect(assistant?.parts.some((part) => part.type === 'data-thinking')).toBe(false)
    expect(assistant?.parts.find((part) => part.type === 'reasoning')).toMatchObject({
      text: 'Planning',
    })
    session.disconnect()
  })

  it('streams deltas for turns started elsewhere into their own message', async () => {
    const session = startSession()
    await connection(0)
    const stream = await connection(0)

    stream.push({
      event: 'model_output_delta',
      data: delta(
        1,
        { kind: 'text_delta', block_index: 0, delta: 'External update' },
        { turn_id: 'external-turn' },
      ),
    })
    const snapshot = await waitForSnapshot(session, (s) => s.messages.length === 1)

    expect(snapshot.messages[0]).toMatchObject({ id: 'turn:external-turn', role: 'assistant' })
    expect(messageText(snapshot.messages[0])).toEqual(['External update'])
    session.disconnect()
  })

  it('ends the turn when a control event lands', async () => {
    const session = startSession()
    await connection(0)
    const stream = await connection(0)

    stream.push({ event: 'agent_input', data: userInputEvent() })
    stream.push({
      event: 'model_output_delta',
      data: delta(1, { kind: 'text_delta', block_index: 0, delta: 'Working' }),
    })
    await waitForSnapshot(session, (s) => s.status === 'streaming')

    stream.push({
      event: 'agent_input',
      data: controlEvent({ sequence: 12 }),
    })
    const finished = await waitForSnapshot(session, (s) => s.status === 'ready')
    expect(finished.messages.some((message) => message.id === 'turn:turn')).toBe(false)
    session.disconnect()
  })

  it('settles to ready when canceled tool results trail the control event', async () => {
    const session = startSession()
    await connection(0)
    const stream = await connection(0)

    stream.push({ event: 'agent_input', data: userInputEvent() })
    stream.push({
      event: 'model_output',
      data: event({
        sequence: 12,
        content_blocks: [toolCallBlock()],
      }),
    })
    await waitForSnapshot(session, (s) => s.status === 'streaming')

    stream.push({
      event: 'agent_input',
      data: controlEvent({ sequence: 13 }),
    })
    stream.push({
      event: 'tool_result',
      data: toolResultEvent({
        sequence: 14,
        tool_call_id: 'call',
        outcome: 'canceled',
        content_blocks: [{ type: 'text', text: 'canceled' }],
      }),
    })
    const finished = await waitForSnapshot(session, (s) => s.status === 'ready')
    expect(finished.status).toBe('ready')
    session.disconnect()
  })

  it('invalidates the interactions query when tool activity streams', async () => {
    const queryClient = new QueryClient()
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries').mockResolvedValue()
    const session = startSession([], client(), queryClient)
    const stream = await connection(0)

    stream.push({ event: 'agent_input', data: userInputEvent() })
    await waitForSnapshot(session, (s) => s.status === 'streaming')
    expect(invalidate).not.toHaveBeenCalled()

    stream.push({
      event: 'model_output',
      data: event({
        sequence: 12,
        content_blocks: [toolCallBlock()],
      }),
    })
    await vi.waitFor(() => {
      expect(invalidate).toHaveBeenCalledTimes(1)
    })

    stream.push({
      event: 'tool_result',
      data: toolResultEvent({
        sequence: 13,
        tool_call_id: 'call',
        content_blocks: [{ type: 'text', text: '/workspace' }],
      }),
    })
    await vi.waitFor(() => {
      expect(invalidate).toHaveBeenCalledTimes(2)
    })

    stream.push({
      event: 'agent_input',
      data: controlEvent({ sequence: 14 }),
    })
    await vi.waitFor(() => {
      expect(invalidate).toHaveBeenCalledTimes(3)
    })
    session.disconnect()
  })

  it('reconnects a dropped stream from the durable cursor', async () => {
    const session = startSession()
    await connection(0)
    const first = await connection(0)

    first.push({ event: 'agent_input', data: userInputEvent() })
    await waitForSnapshot(session, (s) => s.status === 'streaming')
    first.end()

    const second = await connection(1)
    expect(sdkMocks.openAgentEventStream.mock.calls[1]?.[0]).toEqual(
      expect.objectContaining({ query: { after_sequence: 11, stream_deltas: true } }),
    )
    second.push({
      event: 'model_output',
      data: event({ sequence: 12, content_blocks: [{ type: 'text', text: 'Done' }] }),
    })
    const finished = await waitForSnapshot(session, (s) => s.status === 'ready')
    expect(messageText(finished.messages.at(-1))).toEqual(['Done'])
    session.disconnect()
  })

  it('surfaces a fatal stream response instead of retrying it', async () => {
    const session = startSession([], client([401]))
    const snapshot = await waitForSnapshot(session, (s) => s.status === 'error')

    expect(snapshot.error?.message).toBe('Agent event stream request failed with HTTP 401')
    expect(sdkMocks.openAgentEventStream).toHaveBeenCalledTimes(1)
    session.disconnect()
  })

  it('surfaces an in-band internal error without reconnecting', async () => {
    const session = startSession()
    const stream = await connection(0)
    stream.push({
      event: 'error',
      data: { code: 'internal_error', error: 'event projection failed' },
    })
    stream.end()

    const snapshot = await waitForSnapshot(session, (state) => state.status === 'error')
    expect(snapshot.error?.message).toBe('event projection failed')
    expect(sdkMocks.openAgentEventStream).toHaveBeenCalledTimes(1)
    session.disconnect()
  })

  it('backs off repeated retryable failures and resets after a valid frame', async () => {
    const setTimeout = vi.spyOn(globalThis, 'setTimeout')
    const session = startSession([], client([503, 503, 200, 200]))
    const recovered = await connection(2)
    recovered.push({ event: 'agent_input', data: userInputEvent() })
    await waitForSnapshot(session, (state) => state.status === 'streaming')
    recovered.end()
    await connection(3)

    const reconnectDelays = setTimeout.mock.calls
      .map((call) => call[1])
      .filter((delay) => delay === 1 || delay === 2)
    expect(reconnectDelays.slice(-3)).toEqual([1, 2, 1])
    expect(read(session).error).toBeUndefined()
    expect(sdkMocks.openAgentEventStream).toHaveBeenCalledTimes(4)
    session.disconnect()
    setTimeout.mockRestore()
  })

  it('backs off repeated in-band service errors and resets after a valid frame', async () => {
    const setTimeout = vi.spyOn(globalThis, 'setTimeout')
    const session = startSession()
    const first = await connection(0)
    first.push({
      event: 'error',
      data: { code: 'service_unavailable', error: 'event stream unavailable' },
    })
    first.end()
    const second = await connection(1)
    second.push({
      event: 'error',
      data: { code: 'service_unavailable', error: 'event stream unavailable' },
    })
    second.end()
    const recovered = await connection(2)
    recovered.push({ event: 'agent_input', data: userInputEvent() })
    await waitForSnapshot(session, (state) => state.status === 'streaming')
    recovered.end()
    await connection(3)

    const reconnectDelays = setTimeout.mock.calls
      .map((call) => call[1])
      .filter((delay) => delay === 1 || delay === 2)
    expect(reconnectDelays.slice(-3)).toEqual([1, 2, 1])
    expect(read(session).error).toBeUndefined()
    expect(sdkMocks.openAgentEventStream).toHaveBeenCalledTimes(4)
    session.disconnect()
    setTimeout.mockRestore()
  })

  it('surfaces a stream contract violation without reconnecting', async () => {
    const session = startSession()
    const stream = await connection(0)
    stream.fail(
      new AgentEventStreamError({
        kind: 'contract',
        message: 'Agent event stream received data that does not match the API contract',
        retryable: false,
      }),
    )

    const snapshot = await waitForSnapshot(session, (state) => state.status === 'error')
    expect(snapshot.error?.message).toBe(
      'Agent event stream received data that does not match the API contract',
    )
    expect(sdkMocks.openAgentEventStream).toHaveBeenCalledTimes(1)
    session.disconnect()
  })
})

describe('projectAgentChat delta previews', () => {
  const emptyData = { events: [], pendingInput: null, error: undefined, hasOlderEvents: false }

  function previewDelta(
    seq: number,
    text: string,
    overrides: Partial<ModelOutputDelta> = {},
  ): ModelOutputDelta {
    return delta(seq, { kind: 'text_delta', block_index: 0, delta: text }, overrides)
  }

  it('previews a call whose frame and source chains are complete', () => {
    const { messages } = projectAgentChat({
      ...emptyData,
      deltas: [
        previewDelta(1, 'Hello '),
        previewDelta(2, 'wide ', { source_seq_start: 2, source_seq_end: 4 }),
        previewDelta(3, 'world', { source_seq_start: 5, source_seq_end: 5 }),
      ],
    })
    expect(messageText(messages.at(-1))).toEqual(['Hello wide world'])
  })

  it('withholds the preview when a frame seq is skipped', () => {
    const { messages } = projectAgentChat({
      ...emptyData,
      deltas: [previewDelta(1, 'Hello '), previewDelta(3, 'world')],
    })
    expect(messages).toEqual([])
  })

  it('withholds the preview when source events were dropped before framing', () => {
    const { messages } = projectAgentChat({
      ...emptyData,
      deltas: [
        previewDelta(1, 'Hello '),
        previewDelta(2, 'world', { source_seq_start: 3, source_seq_end: 3 }),
      ],
    })
    expect(messages).toEqual([])
  })

  it('keeps isWorking true through a client-side error mid-turn', () => {
    const { status, isWorking } = projectAgentChat({
      ...emptyData,
      events: [userInputEvent()],
      deltas: [],
      error: new Error('stream failed'),
    })
    expect(status).toBe('error')
    expect(isWorking).toBe(true)
  })
})

describe('agentEventsToMessages', () => {
  it('uses omnara_display_text for user text blocks', () => {
    const messages = agentEventsToMessages([
      userInputEvent({
        content_blocks: [
          {
            type: 'text',
            text: 'ask <@U123> (Ada) about <#C123> (#general)',
            metadata: { omnara_display_text: 'ask @Ada about #general' },
          },
        ],
      }),
    ])

    expect(messages).toMatchObject([
      {
        role: 'user',
        parts: [{ type: 'text', text: 'ask @Ada about #general' }],
      },
    ])
  })

  it('hides content blocks explicitly marked omnara_hidden', () => {
    const messages = agentEventsToMessages([
      userInputEvent({
        id: 'input',
        sequence: 10,
        content_blocks: [
          { type: 'text', text: 'Slack context', metadata: { omnara_hidden: true } },
          { type: 'text', text: 'Inspect the workspace' },
          {
            type: 'media_ref',
            artifact_id: 'hidden_input_media',
            metadata: { omnara_hidden: true },
          },
        ],
      }),
      event({
        id: 'call-event',
        sequence: 11,
        content_blocks: [
          { type: 'reasoning', text: 'private', metadata: { omnara_hidden: true } },
          toolCallBlock(),
          { type: 'text', text: 'Working' },
        ],
      }),
      toolResultEvent({
        id: 'result-event',
        sequence: 12,
        content_blocks: [
          {
            type: 'structured_data',
            value: { private: true },
            metadata: { omnara_hidden: true },
          },
          { type: 'text', text: 'visible result' },
        ],
      }),
    ])

    expect(messages).toMatchObject([
      {
        role: 'user',
        parts: [{ type: 'text', text: 'Inspect the workspace' }],
      },
      {
        role: 'assistant',
        parts: [
          {
            type: 'dynamic-tool',
            output: {
              contentBlocks: [{ type: 'text', text: 'visible result' }],
            },
          },
          { type: 'text', text: 'Working' },
        ],
      },
    ])
  })

  it('represents media_ref blocks as attachment indicators', () => {
    const messages = agentEventsToMessages([
      userInputEvent({
        id: 'media-input',
        sequence: 10,
        content_blocks: [{ type: 'media_ref', artifact_id: 'art_media' }],
      }),
      event({
        id: 'media-output',
        sequence: 11,
        content_blocks: [{ type: 'media_ref', artifact_id: 'art_render' }],
      }),
    ])

    expect(messages).toMatchObject([
      {
        id: 'media-input',
        role: 'user',
        parts: [{ type: 'data-media', data: { artifactId: 'art_media' } }],
      },
      {
        id: 'turn:turn',
        role: 'assistant',
        parts: [{ type: 'data-media', data: { artifactId: 'art_render' } }],
      },
    ])
  })

  it('represents initial and subsequent config changes as timeline markers', () => {
    const messages = agentEventsToMessages([
      configChangeEvent({
        id: 'initial-config',
        sequence: 1,
        turn_id: 'initial-config-turn',
        turn_sequence: 1,
        is_opening_event: true,
      }),
      configChangeEvent({
        id: 'updated-config',
        sequence: 8,
        turn_id: 'updated-config-turn',
        turn_sequence: 3,
        is_opening_event: true,
      }),
    ])

    expect(messages).toMatchObject([
      {
        id: 'initial-config',
        metadata: { inputKind: 'config_change' },
        parts: [{ type: 'data-agent-config', data: { action: 'initialized' } }],
      },
      {
        id: 'updated-config',
        metadata: { inputKind: 'config_change' },
        parts: [{ type: 'data-agent-config', data: { action: 'changed' } }],
      },
    ])
  })

  it('never labels a config change as initialized while older history may be unloaded', () => {
    const messages = agentEventsToMessages(
      [
        configChangeEvent({
          id: 'earliest-loaded-config',
          sequence: 8,
          turn_id: 'config-turn',
          turn_sequence: 3,
          is_opening_event: true,
        }),
      ],
      { hasOlderEvents: true },
    )

    expect(messages).toMatchObject([
      {
        id: 'earliest-loaded-config',
        parts: [{ type: 'data-agent-config', data: { action: 'changed' } }],
      },
    ])
  })

  it('groups a tool loop into one assistant UI message and applies its result', () => {
    const messages = agentEventsToMessages([
      userInputEvent({
        id: 'input',
        sequence: 10,
        content_blocks: [{ type: 'text', text: 'Inspect the workspace' }],
      }),
      event({
        id: 'call-event',
        sequence: 11,
        content_blocks: [toolCallBlock()],
      }),
      toolResultEvent({
        id: 'result-event',
        sequence: 12,
        tool_call_id: 'call',
        outcome: 'denied',
        content_blocks: [{ type: 'structured_data', value: { reason: 'tool call was denied' } }],
      }),
      event({
        id: 'answer-event',
        sequence: 13,
        content_blocks: [{ type: 'text', text: 'Done' }],
      }),
    ])

    expect(messages).toHaveLength(2)
    expect(messages[1]).toMatchObject({
      id: 'turn:turn',
      role: 'assistant',
      metadata: { sequence: 13 },
      parts: [
        {
          type: 'dynamic-tool',
          toolCallId: 'call',
          state: 'output-available',
          output: {
            outcome: 'denied',
            contentBlocks: [{ type: 'structured_data', value: { reason: 'tool call was denied' } }],
          },
        },
        { type: 'step-start' },
        { type: 'text', text: 'Done' },
      ],
    })
  })
})
