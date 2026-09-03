import type {
  AgentEvent,
  AgentEventStreamFrame,
  AgentInteraction,
  InteractionAnswer,
} from '@omnara/sdk'

import { blockText, summaryWidth, toolCallSummary } from './agent-rendering.ts'
import { abbreviate } from './output.ts'

export interface ToolCallInfo {
  name: string
  summary?: string
}

export type InteractionKindLabel = 'approval' | 'question'

export type TranscriptEntryBody =
  | { kind: 'user'; text: string }
  | { kind: 'agent'; text: string }
  | { kind: 'tool'; name: string; outcome: string; summary?: string; output?: string }
  | { kind: 'checkpoint' }
  | { kind: 'error'; label: string; text: string }
  | { kind: 'note'; text: string }
  | { kind: 'answer'; kindLabel: InteractionKindLabel; title: string; lines: string[] }

export type TranscriptEntry = { id: number } & TranscriptEntryBody

export interface AgentActivity {
  text: string
  detail?: string
}

export interface ChatState {
  nextId: number
  entries: TranscriptEntry[]
  streaming: { contextId: string; text: string } | undefined
  toolCalls: Record<string, ToolCallInfo>
  activity: AgentActivity | undefined
  workStart: number | undefined
  thinkingTail: string
  interactions: AgentInteraction[]
  ready: boolean
  ended: boolean
}

export type ChatAction =
  | { type: 'history'; events: AgentEvent[]; truncated: boolean }
  | { type: 'frame'; frame: AgentEventStreamFrame; now: number }
  | { type: 'reconnecting' }
  | { type: 'stream_failed'; message: string }
  | { type: 'sent'; text: string; now: number }
  | { type: 'interactions'; interactions: AgentInteraction[] }
  | { type: 'answered'; interaction: AgentInteraction; answers: InteractionAnswer[]; now: number }
  | { type: 'answer_failed'; interaction: AgentInteraction; message: string }
  | { type: 'error'; label: string; message: string }

export function interactionKindLabel(interaction: AgentInteraction): InteractionKindLabel {
  return interaction.interaction_kind === 'permission' ? 'approval' : 'question'
}

export function formatDuration(ms: number): string {
  const seconds = Math.max(1, Math.round(ms / 1000))
  if (seconds < 60) return `${seconds}s`
  return `${Math.floor(seconds / 60)}m ${String(seconds % 60).padStart(2, '0')}s`
}

export function initialChatState(): ChatState {
  return {
    nextId: 1,
    entries: [],
    streaming: undefined,
    toolCalls: {},
    activity: undefined,
    workStart: undefined,
    thinkingTail: '',
    interactions: [],
    ready: false,
    ended: false,
  }
}

function withEntries(state: ChatState, entries: TranscriptEntryBody[]): ChatState {
  if (entries.length === 0) return state
  let nextId = state.nextId
  const added = entries.map((entry) => ({ ...entry, id: nextId++ }))
  return { ...state, nextId, entries: [...state.entries, ...added] }
}

function textOf(blocks: { type: string; text?: string }[]): string {
  return blocks.map((block) => (block.type === 'text' ? (block.text ?? '') : '')).join('')
}

function startWork(state: ChatState, text: string, now: number, detail?: string): ChatState {
  return {
    ...state,
    activity: { text, detail },
    workStart: state.workStart ?? now,
  }
}

function finishTurn(state: ChatState, now: number): ChatState {
  const next = { ...state, activity: undefined, workStart: undefined, thinkingTail: '' }
  if (state.workStart == null) return next
  const elapsed = now - state.workStart
  if (elapsed < 1000) return next
  return withEntries(next, [{ kind: 'note', text: `✔ Worked for ${formatDuration(elapsed)}` }])
}

function endTurn(state: ChatState, stopReason: string | null | undefined, now: number): ChatState {
  return stopReason === 'tool_use'
    ? startWork(state, 'running tools…', now)
    : finishTurn(state, now)
}

function applyEvent(state: ChatState, event: AgentEvent, showInput: boolean): ChatState {
  switch (event.event_kind) {
    case 'agent_input': {
      if (!showInput || event.input_kind !== 'content') return state
      const text = event.content_blocks
        .map((block) => (block.type === 'text' ? block.text : ''))
        .filter((line) => line.trim() !== '')
        .join('\n')
      return text === '' ? state : withEntries(state, [{ kind: 'user', text }])
    }
    case 'model_output': {
      const entries: TranscriptEntryBody[] = []
      const text = textOf(event.content_blocks)
      if (text.trim() !== '') entries.push({ kind: 'agent', text })
      const toolCalls = { ...state.toolCalls }
      for (const block of event.content_blocks) {
        if (block.type === 'tool_call') {
          toolCalls[block.tool_call_id] = {
            name: block.name,
            summary: toolCallSummary(block.name, block.input),
          }
        } else if (block.type === 'error') {
          entries.push({ kind: 'error', label: 'error', text: block.text })
        }
      }
      const streaming =
        state.streaming?.contextId === event.model_call_context_id ? undefined : state.streaming
      return withEntries({ ...state, toolCalls, streaming }, entries)
    }
    case 'tool_result': {
      const { [event.tool_call_id]: call, ...toolCalls } = state.toolCalls
      const output = event.content_blocks
        .filter((block) => block.type === 'text' || block.type === 'structured_data')
        .map(blockText)
        .find((text) => text.trim() !== '')
      return withEntries({ ...state, toolCalls }, [
        {
          kind: 'tool',
          name: call?.name ?? event.tool_call_id,
          outcome: event.outcome,
          summary: call?.summary,
          output: output == null ? undefined : abbreviate(output, summaryWidth),
        },
      ])
    }
    case 'context_checkpoint':
      return withEntries(state, [{ kind: 'checkpoint' }])
  }
}

function startsWork(frame: AgentEventStreamFrame): boolean {
  if (!('event_kind' in frame)) return false
  if (frame.event_kind === 'tool_result') return true
  return (
    frame.event_kind === 'agent_input' &&
    (frame.input_kind === 'content' || frame.input_kind === 'interaction_response')
  )
}

function applyFrame(state: ChatState, frame: AgentEventStreamFrame, now: number): ChatState {
  let next = startsWork(frame) ? startWork(state, 'working…', now) : state
  if ('event_kind' in frame) {
    next = applyEvent(next, frame, false)
    return frame.event_kind === 'model_output' ? endTurn(next, frame.stop_reason, now) : next
  }
  if (!('event' in frame)) return next
  const delta = frame.event
  switch (delta.kind) {
    case 'block_start':
      if (delta.block.kind === 'tool_use') {
        const { tool_call_id, tool_name } = delta.block
        if (!(tool_call_id in next.toolCalls)) {
          next = { ...next, toolCalls: { ...next.toolCalls, [tool_call_id]: { name: tool_name } } }
        }
        return startWork(next, `calling ${tool_name}…`, now)
      }
      if (delta.block.kind === 'thinking') {
        return startWork({ ...next, thinkingTail: '' }, 'thinking…', now)
      }
      return startWork(next, 'writing…', now)
    case 'thinking_delta': {
      const thinkingTail = (next.thinkingTail + delta.delta).slice(-200)
      return startWork({ ...next, thinkingTail }, 'thinking…', now, thinkingTail)
    }
    case 'text_delta': {
      const contextId = frame.model_call_context_id
      const streaming = next.streaming
      const previous = streaming?.contextId === contextId ? streaming.text : ''
      return startWork(
        { ...next, streaming: { contextId, text: previous + delta.delta } },
        'writing…',
        now,
      )
    }
    case 'message_stop':
      return endTurn({ ...next, thinkingTail: '' }, delta.stop.reason, now)
    case 'error':
      return withEntries(finishTurn(next, now), [
        { kind: 'error', label: 'error', text: delta.error.message },
      ])
    default:
      return next
  }
}

function answerLines(interaction: AgentInteraction, answers: InteractionAnswer[]): string[] {
  return interaction.request.questions.map((question, index) => {
    const answer = answers[index]
    if (answer == null) return question.prompt
    const chosen = answer.option_indices
      .map((optionIndex) => question.options[optionIndex]?.label)
      .join(', ')
    return `${question.prompt} ${chosen}${answer.text != null ? `: ${answer.text}` : ''}`
  })
}

export function reduceChat(state: ChatState, action: ChatAction): ChatState {
  switch (action.type) {
    case 'history': {
      let next = withEntries(
        state,
        action.truncated ? [{ kind: 'note', text: '(older history omitted)' }] : [],
      )
      for (const event of action.events) next = applyEvent(next, event, true)
      return { ...next, ready: true }
    }
    case 'frame':
      return applyFrame(state, action.frame, action.now)
    case 'reconnecting':
      return { ...state, streaming: undefined }
    case 'stream_failed':
      return withEntries({ ...state, streaming: undefined, activity: undefined, ended: true }, [
        { kind: 'error', label: 'stream error', text: action.message },
      ])
    case 'sent':
      return startWork(
        withEntries(state, [{ kind: 'user', text: action.text }]),
        'working…',
        action.now,
      )
    case 'interactions': {
      if (action.interactions.length === 0) return state
      return {
        ...state,
        interactions: [...state.interactions, ...action.interactions],
        activity: undefined,
        workStart: undefined,
      }
    }
    case 'answered': {
      const interactions = state.interactions.filter((item) => item.id !== action.interaction.id)
      const entry: TranscriptEntryBody = {
        kind: 'answer',
        kindLabel: interactionKindLabel(action.interaction),
        title: action.interaction.request.title,
        lines: answerLines(action.interaction, action.answers),
      }
      return startWork(withEntries({ ...state, interactions }, [entry]), 'working…', action.now)
    }
    case 'answer_failed': {
      const others = state.interactions.filter((item) => item.id !== action.interaction.id)
      return withEntries({ ...state, interactions: [...others, action.interaction] }, [
        { kind: 'error', label: 'error', text: action.message },
      ])
    }
    case 'error':
      return withEntries(state, [{ kind: 'error', label: action.label, text: action.message }])
  }
}
