import type { AgentEvent, ModelOutputDelta, ModelOutputStreamDelta } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import { type ChatAction, type ChatState, initialChatState, reduceChat } from './chat-state.ts'

const base = {
  id: 'evt',
  org_id: 'org',
  project_id: 'proj',
  agent_id: 'agent',
  turn_id: 'turn',
  turn_sequence: 1,
  created_at: '2026-01-01T00:00:00Z',
}

function modelOutput(
  sequence: number,
  contextId: string,
  text: string,
  stopReason: 'end_turn' | 'tool_use' = 'end_turn',
): AgentEvent {
  return {
    ...base,
    is_opening_event: false,
    sequence,
    event_kind: 'model_output',
    model_call_context_id: contextId,
    stop_reason: stopReason,
    content_blocks: [{ type: 'text', text }],
  }
}

function delta(contextId: string, event: ModelOutputStreamDelta): ModelOutputDelta {
  return {
    turn_id: 'turn',
    model_call_context_id: contextId,
    seq: 1,
    source_seq_start: 1,
    source_seq_end: 1,
    coalesced_count: 1,
    event,
  }
}

function run(actions: ChatAction[], start: ChatState = initialChatState()): ChatState {
  return actions.reduce(reduceChat, start)
}

describe('reduceChat', () => {
  it('replaces streamed text with the durable model output', () => {
    const state = run([
      {
        type: 'frame',
        frame: delta('ctx', { kind: 'text_delta', block_index: 0, delta: 'Hel' }),
        now: 0,
      },
      {
        type: 'frame',
        frame: delta('ctx', { kind: 'text_delta', block_index: 0, delta: 'lo' }),
        now: 0,
      },
    ])
    expect(state.streaming).toEqual({ contextId: 'ctx', text: 'Hello' })
    expect(state.activity?.text).toBe('writing…')

    const done = reduceChat(state, {
      type: 'frame',
      frame: modelOutput(1, 'ctx', 'Hello world'),
      now: 5000,
    })
    expect(done.streaming).toBeUndefined()
    expect(done.entries.map((entry) => entry.kind)).toEqual(['agent', 'note'])
    expect(done.entries[0]).toMatchObject({ kind: 'agent', text: 'Hello world' })
    expect(done.entries[1]).toMatchObject({ kind: 'note', text: '✔ Worked for 5s' })
    expect(done.activity).toBeUndefined()
  })

  it('keeps working while tools run and names them in results', () => {
    const state = run([
      {
        type: 'frame',
        frame: delta('ctx', {
          kind: 'block_start',
          block_index: 0,
          block: { kind: 'tool_use', tool_call_id: 'call-1', tool_name: 'run_command' },
        }),
        now: 0,
      },
      { type: 'frame', frame: modelOutput(1, 'ctx', '', 'tool_use'), now: 0 },
    ])
    expect(state.activity?.text).toBe('running tools…')
    expect(state.toolCalls['call-1']).toEqual({ name: 'run_command' })

    const done = reduceChat(state, {
      type: 'frame',
      frame: {
        ...base,
        is_opening_event: false,
        sequence: 2,
        event_kind: 'tool_result',
        tool_call_id: 'call-1',
        outcome: 'succeeded',
        content_blocks: [{ type: 'text', text: 'ok' }],
      },
      now: 0,
    })
    expect(done.toolCalls).toEqual({})
    expect(done.entries.at(-1)).toMatchObject({
      kind: 'tool',
      name: 'run_command',
      outcome: 'succeeded',
      output: 'ok',
    })
    expect(done.activity?.text).toBe('working…')
  })

  it('drops partial streamed text on reconnect and leaves the durable event to reprint it', () => {
    const state = run([
      {
        type: 'frame',
        frame: delta('ctx', { kind: 'text_delta', block_index: 0, delta: 'partial' }),
        now: 0,
      },
      { type: 'reconnecting' },
      { type: 'frame', frame: modelOutput(1, 'ctx', 'partial and complete'), now: 0 },
    ])
    expect(state.streaming).toBeUndefined()
    expect(state.entries).toHaveLength(1)
    expect(state.entries[0]).toMatchObject({ kind: 'agent', text: 'partial and complete' })
  })

  it('pauses the work timer while an interaction waits and restarts it on answer', () => {
    const interaction = {
      id: 'int-1',
      org_id: 'org',
      project_id: 'proj',
      agent_id: 'agent',
      turn_id: 'turn',
      tool_call_id: 'call-1',
      interaction_kind: 'question' as const,
      state: 'open' as const,
      request: {
        title: 'Pick one',
        questions: [{ prompt: 'Which?', options: [{ label: 'A' }, { label: 'B' }] }],
      },
      created_at: '2026-01-01T00:00:00Z',
    }
    const waiting = run([
      { type: 'sent', text: 'hi', now: 0 },
      { type: 'interactions', interactions: [interaction] },
    ])
    expect(waiting.activity).toBeUndefined()
    expect(waiting.workStart).toBeUndefined()
    expect(waiting.interactions).toHaveLength(1)

    const answered = reduceChat(waiting, {
      type: 'answered',
      interaction,
      answers: [{ option_indices: [1] }],
      now: 10_000,
    })
    expect(answered.interactions).toHaveLength(0)
    expect(answered.workStart).toBe(10_000)
    expect(answered.entries.at(-1)).toMatchObject({
      kind: 'answer',
      title: 'Pick one',
      lines: ['Which? B'],
    })
  })

  it('requeues an interaction whose answer failed', () => {
    const interaction = {
      id: 'int-1',
      org_id: 'org',
      project_id: 'proj',
      agent_id: 'agent',
      turn_id: 'turn',
      tool_call_id: 'call-1',
      interaction_kind: 'permission' as const,
      state: 'open' as const,
      request: { title: 'Allow?', questions: [] },
      created_at: '2026-01-01T00:00:00Z',
    }
    const state = run([
      { type: 'interactions', interactions: [interaction] },
      { type: 'answer_failed', interaction, message: 'boom' },
    ])
    expect(state.interactions.map((item) => item.id)).toEqual(['int-1'])
    expect(state.entries.at(-1)).toMatchObject({ kind: 'error', text: 'boom' })
  })

  it('shows user input only from history', () => {
    const input: AgentEvent = {
      ...base,
      is_opening_event: true,
      sequence: 1,
      event_kind: 'agent_input',
      agent_input_id: 'in-1',
      input_kind: 'content',
      content_blocks: [{ type: 'text', text: 'hello' }],
    }
    expect(run([{ type: 'history', events: [input], truncated: false }]).entries).toMatchObject([
      { kind: 'user', text: 'hello' },
    ])
    expect(run([{ type: 'frame', frame: input, now: 0 }]).entries).toEqual([])
  })
})
