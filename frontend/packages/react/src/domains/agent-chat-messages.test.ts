import { describe, expect, it } from 'vitest'

import {
  agentEventsToMessages,
  type ModelOutputDelta,
  projectAgentChat,
} from './agent-chat-messages'
import {
  configChangeEvent,
  delta,
  event,
  messageText,
  toolCallBlock,
  toolResultEvent,
  userInputEvent,
} from './agent-chat-test-support'

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

  it('withholds the preview when loss hides inside one frame', () => {
    const { messages } = projectAgentChat({
      ...emptyData,
      deltas: [
        previewDelta(1, 'Hello '),
        previewDelta(2, 'world', {
          source_seq_start: 2,
          source_seq_end: 4,
          coalesced_count: 2,
        }),
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

  it('keeps part ids unique and stable when settled output and previews share a turn', () => {
    const deltas = [
      delta(
        1,
        { kind: 'block_start', block_index: 0, block: { kind: 'text' } },
        { model_call_context_id: 'preview-call' },
      ),
      delta(
        2,
        { kind: 'text_delta', block_index: 0, delta: 'Streaming' },
        {
          model_call_context_id: 'preview-call',
        },
      ),
      delta(
        3,
        { kind: 'block_start', block_index: 1, block: { kind: 'thinking' } },
        { model_call_context_id: 'preview-call' },
      ),
    ]
    const data = {
      ...emptyData,
      events: [
        event({
          id: 'settled-output',
          content_blocks: [{ type: 'text' as const, text: 'Settled' }],
        }),
      ],
      deltas,
    }

    const firstParts = projectAgentChat(data).messages.at(-1)?.parts ?? []
    const firstIds = firstParts.map((part) => part.id)
    const updatedParts = projectAgentChat({
      ...data,
      deltas: [
        ...deltas,
        delta(
          4,
          { kind: 'thinking_delta', block_index: 1, delta: 'Planning' },
          {
            model_call_context_id: 'preview-call',
          },
        ),
      ],
    }).messages.at(-1)?.parts

    expect(firstIds).toEqual(['mcc:block:0', 'preview-call:block:0', 'preview-call:block:1'])
    expect(new Set(firstIds).size).toBe(firstIds.length)
    expect(updatedParts?.map((part) => part.id)).toEqual(firstIds)
    expect(updatedParts?.at(-1)).toMatchObject({ type: 'reasoning', text: 'Planning' })
  })

  it('keeps part ids stable when a streamed model call settles', () => {
    const deltas = [
      delta(1, { kind: 'block_start', block_index: 0, block: { kind: 'thinking' } }),
      delta(2, { kind: 'thinking_delta', block_index: 0, delta: 'Planning' }),
      delta(3, {
        kind: 'block_start',
        block_index: 1,
        block: { kind: 'tool_use', tool_call_id: 'call', tool_name: 'shell' },
      }),
      delta(4, { kind: 'tool_arguments_delta', block_index: 1, delta: '{"command":"pwd"}' }),
    ]
    const previewParts = projectAgentChat({
      ...emptyData,
      events: [userInputEvent()],
      deltas,
    }).messages.at(-1)?.parts
    const settledParts = projectAgentChat({
      ...emptyData,
      events: [
        userInputEvent(),
        event({
          id: 'settled-output',
          sequence: 12,
          content_blocks: [{ type: 'reasoning', text: 'Planning' }, toolCallBlock()],
        }),
      ],
      deltas: [],
    }).messages.at(-1)?.parts

    expect(previewParts?.map((part) => part.id)).toEqual(['mcc:block:0', 'mcc:block:1'])
    expect(settledParts?.map((part) => part.id)).toEqual(previewParts?.map((part) => part.id))
    expect(settledParts).toMatchObject([
      { type: 'reasoning', text: 'Planning' },
      { type: 'dynamic-tool', toolCallId: 'call', input: { command: 'pwd' } },
    ])
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

  it('represents durable model errors as error message parts', () => {
    const messages = agentEventsToMessages([
      event({
        stop_reason: 'error',
        content_blocks: [{ type: 'error', text: 'The model provider request failed.' }],
      }),
    ])

    expect(messages).toMatchObject([
      {
        role: 'assistant',
        parts: [
          {
            type: 'data-model-error',
            id: 'mcc:block:0',
            data: { text: 'The model provider request failed.' },
          },
        ],
      },
    ])
  })

  it('exposes failed built-in tool error codes without trusting external tools', () => {
    const result = toolResultEvent({
      outcome: 'failed',
      content_blocks: [
        {
          type: 'structured_data',
          value: { error_code: 'managed_work_admission_denied' },
        },
      ],
    })
    const builtIn = agentEventsToMessages([event({ content_blocks: [toolCallBlock()] }), result])
    expect(builtIn[0]?.parts[0]).toMatchObject({
      type: 'dynamic-tool',
      toolType: 'built_in',
      toolErrorCode: 'managed_work_admission_denied',
    })
    for (const toolType of ['custom', 'mcp'] as const) {
      const external = agentEventsToMessages([
        event({
          content_blocks: [{ ...toolCallBlock(), tool_type: toolType }],
        }),
        result,
      ])
      expect(external[0]?.parts[0]).toMatchObject({ type: 'dynamic-tool', toolType })
      expect(external[0]?.parts[0]).not.toHaveProperty('toolErrorCode')
    }
  })

  it('hides content blocks explicitly marked omnara_hidden', () => {
    const messages = agentEventsToMessages([
      userInputEvent({
        id: 'input',
        sequence: 10,
        content_blocks: [
          { type: 'text', text: 'Slack context', metadata: { omnara_hidden: 'true' } },
          { type: 'text', text: 'Inspect the workspace' },
          {
            type: 'media_ref',
            artifact_id: 'hidden_input_media',
            metadata: { omnara_hidden: 'true' },
          },
        ],
      }),
      event({
        id: 'call-event',
        sequence: 11,
        content_blocks: [
          { type: 'reasoning', text: 'private', metadata: { omnara_hidden: 'true' } },
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
            metadata: { omnara_hidden: 'true' },
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
