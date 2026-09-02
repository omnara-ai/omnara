import { AgentEventStreamError } from '@omnara/sdk'
import { QueryClient } from '@tanstack/react-query'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import {
  chatSdkMocks,
  client,
  connection,
  controlEvent,
  delta,
  event,
  messageText,
  read,
  resetChatTestHarness,
  startSession,
  toolCallBlock,
  toolResultEvent,
  userInputEvent,
  waitForSnapshot,
} from './agent-chat-test-support'

const sdkMocks = chatSdkMocks()

describe('AgentChatSession streaming', () => {
  beforeEach(resetChatTestHarness)

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

  it('drops a failed model attempt preview while recovery continues', async () => {
    const session = startSession()
    const stream = await connection(0)

    stream.push({ event: 'agent_input', data: userInputEvent() })
    stream.push({
      event: 'model_output_delta',
      data: delta(
        1,
        { kind: 'block_start', block_index: 0, block: { kind: 'text' } },
        { model_call_context_id: 'failed-call' },
      ),
    })
    stream.push({
      event: 'model_output_delta',
      data: delta(
        2,
        { kind: 'text_delta', block_index: 0, delta: 'Partial' },
        { model_call_context_id: 'failed-call' },
      ),
    })
    await waitForSnapshot(session, (state) => messageText(state.messages.at(-1))[0] === 'Partial')

    stream.push({
      event: 'model_output_delta',
      data: delta(
        3,
        { kind: 'error', error: { message: 'model call failed' } },
        { model_call_context_id: 'failed-call' },
      ),
    })
    const retrying = await waitForSnapshot(
      session,
      (state) =>
        state.status === 'streaming' &&
        !state.messages.some((message) => message.id === 'turn:turn'),
    )
    expect(retrying.error).toBeUndefined()

    stream.push({
      event: 'model_output',
      data: event({
        model_call_context_id: 'recovered-call',
        sequence: 12,
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
    await vi.waitFor(() => {
      expect(invalidate).toHaveBeenCalledTimes(1)
    })
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: [expect.objectContaining({ _id: 'listQueuedBacklogInputs' })],
    })
    invalidate.mockClear()

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

  it('clears connection-scoped previews and accepts recovered durable output', async () => {
    const queryClient = new QueryClient()
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries')
    const session = startSession([], client(), queryClient)
    const stream = await connection(0)

    stream.push({ event: 'agent_input', data: userInputEvent() })
    stream.push({
      event: 'model_output_delta',
      data: delta(1, { kind: 'text_delta', block_index: 0, delta: 'Partial' }),
    })
    await waitForSnapshot(session, (s) => messageText(s.messages.at(-1))[0] === 'Partial')

    stream.connectionState({
      state: 'reconnecting',
      attempt: 1,
      delayMs: 100,
      error: new AgentEventStreamError({
        kind: 'transport',
        message: 'disconnected',
      }),
    })
    await vi.waitFor(() => {
      expect(read(session).messages.flatMap((message) => messageText(message))).not.toContain(
        'Partial',
      )
    })
    const invalidationsBeforeReconnect = invalidate.mock.calls.length
    stream.connectionState({ state: 'connected', reconnected: true })
    await vi.waitFor(() => {
      expect(invalidate).toHaveBeenCalledTimes(invalidationsBeforeReconnect + 1)
    })

    stream.push({
      event: 'model_output',
      data: event({ sequence: 12, content_blocks: [{ type: 'text', text: 'Done' }] }),
    })
    const finished = await waitForSnapshot(session, (s) => s.status === 'ready')
    expect(messageText(finished.messages.at(-1))).toEqual(['Done'])
    expect(sdkMocks.openAgentEventStream).toHaveBeenCalledTimes(1)
    session.disconnect()
  })

  it('opens the continuous follower from the initial durable cursor', async () => {
    const session = startSession()
    await connection(0)
    expect(sdkMocks.openAgentEventStream).toHaveBeenCalledWith(
      expect.objectContaining({ query: { after_sequence: 0, stream_deltas: true } }),
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
    expect(sdkMocks.openAgentEventStream).toHaveBeenCalledTimes(1)
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
    expect(sdkMocks.openAgentEventStream).toHaveBeenCalledTimes(1)
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
    expect(sdkMocks.openAgentEventStream).toHaveBeenCalledTimes(1)
    session.disconnect()
  })
})
