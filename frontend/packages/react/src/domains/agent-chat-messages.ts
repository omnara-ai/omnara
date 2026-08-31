import type {
  AgentEvent,
  AgentEventStreamData,
  AgentInput,
  AgentInputEvent,
  Error as APIError,
  MediaRefContentBlock,
  ModelOutputDelta,
  ModelOutputEvent,
  ToolCallType,
  ToolResultContentBlock,
} from '@omnara/sdk'
import type { UIMessage } from 'ai'

import type {
  AgentChatData,
  AgentChatStatus,
  LocalAgentInput,
  OmnaraMessageMetadata,
} from './agent-chat-types'
import type { AgentInputBacklogItem } from './agent-input-backlog'

export type { ModelOutputDelta } from '@omnara/sdk'

// A type alias preserves the finite data-part keys required for discriminated narrowing.
// eslint-disable-next-line @typescript-eslint/consistent-type-definitions
export type OmnaraUIData = {
  'agent-config': { action: 'initialized' | 'changed' }
  'model-error': { text: string }
  thinking: { active: boolean }
  media: {
    artifactId?: string
    excludeFromModelContext?: MediaRefContentBlock['exclude_from_model_context']
    data?: string
    mediaType?: string
    filename?: string
    sizeBytes?: number
  }
}

type BaseOmnaraUIMessage = UIMessage<OmnaraMessageMetadata, OmnaraUIData>
type OmnaraUIMessagePart = BaseOmnaraUIMessage['parts'][number] & {
  id: string
  toolType?: ToolCallType
  toolErrorCode?: string
}
export type OmnaraUIMessage = Omit<BaseOmnaraUIMessage, 'parts'> & {
  parts: OmnaraUIMessagePart[]
}

export type AgentStreamFrame =
  | { kind: 'event'; event: AgentEvent }
  | { kind: 'tool_call_update' }
  | { kind: 'delta'; delta: ModelOutputDelta }
  | { kind: 'error'; error: APIError }

export function parseStreamData(data: AgentEventStreamData): AgentStreamFrame {
  if ('event_kind' in data) return { kind: 'event', event: data }
  if ('tool_call_id' in data && 'state' in data) return { kind: 'tool_call_update' }
  if ('model_call_context_id' in data) return { kind: 'delta', delta: data }
  return { kind: 'error', error: data }
}

export function sequenceNumber(value: number | undefined): number {
  return value ?? 0
}

export function eventMetadata(event: AgentEvent): OmnaraMessageMetadata {
  return {
    eventId: event.id,
    eventKind: event.event_kind,
    inputKind: event.event_kind === 'agent_input' ? event.input_kind : undefined,
    actorId: event.event_kind === 'agent_input' ? event.actor_id : undefined,
    sequence: sequenceNumber(event.sequence),
    turnId: event.turn_id,
    turnSequence: sequenceNumber(event.turn_sequence),
    createdAt: event.created_at,
  }
}

/** An attachment marker for a media_ref block; the artifact holds the bytes. */
function mediaPart(block: MediaRefContentBlock, id: string): OmnaraUIMessagePart {
  return {
    type: 'data-media',
    id,
    data: {
      artifactId: block.artifact_id,
      excludeFromModelContext: block.exclude_from_model_context,
    },
  }
}

function isHiddenContentBlock(block: { metadata?: Record<string, unknown> }): boolean {
  return block.metadata?.omnara_hidden === 'true'
}

function structuredToolErrorCode(blocks: ToolResultContentBlock[]): string | undefined {
  for (const block of blocks) {
    if (
      block.type !== 'structured_data' ||
      block.value == null ||
      typeof block.value !== 'object'
    ) {
      continue
    }
    if (!('error_code' in block.value)) continue
    const errorCode = block.value.error_code
    if (typeof errorCode === 'string') return errorCode
  }
  return undefined
}

function agentInputParts(event: AgentInputEvent): OmnaraUIMessage['parts'] {
  const parts: OmnaraUIMessage['parts'] = []
  for (const [blockIndex, block] of event.content_blocks.entries()) {
    const id = `${event.id}:block:${String(blockIndex)}`
    if (isHiddenContentBlock(block)) continue
    if (block.type === 'text') {
      const displayText = block.metadata?.omnara_display_text
      parts.push({
        type: 'text',
        id,
        text: typeof displayText === 'string' ? displayText : block.text,
        state: 'done',
      })
    }
    if (block.type === 'media_ref') {
      parts.push(mediaPart(block, id))
    }
  }
  return parts
}

function modelOutputParts(event: ModelOutputEvent): OmnaraUIMessage['parts'] {
  const parts: OmnaraUIMessage['parts'] = []
  for (const [blockIndex, block] of event.content_blocks.entries()) {
    const id = `${event.model_call_context_id}:block:${String(blockIndex)}`
    if (isHiddenContentBlock(block)) continue
    if (block.type === 'text') {
      parts.push({ type: 'text', id, text: block.text, state: 'done' })
    }
    if (block.type === 'reasoning') {
      parts.push({ type: 'reasoning', id, text: block.text, state: 'done' })
    }
    if (block.type === 'media_ref') {
      parts.push(mediaPart(block, id))
    }
    if (block.type === 'tool_call') {
      parts.push({
        type: 'dynamic-tool',
        id,
        toolCallId: block.tool_call_id,
        toolName: block.name,
        toolType: block.tool_type,
        state: 'input-available',
        input: block.input,
      })
    }
    if (block.type === 'error') {
      parts.push({ type: 'data-model-error', id, data: { text: block.text } })
    }
  }
  return parts
}

/**
 * Converts the durable event log into the same UIMessage shape produced by
 * the live transport. `hasOlderEvents` must be true whenever older history
 * might still be unloaded: backward pagination can reveal an earlier config
 * change than any currently loaded, and the first config change in an
 * incomplete window is not necessarily the agent's actual first one.
 */
export function agentEventsToMessages(
  events: AgentEvent[],
  { hasOlderEvents = false }: { hasOlderEvents?: boolean } = {},
): OmnaraUIMessage[] {
  const messages: OmnaraUIMessage[] = []
  const assistantByTurn = new Map<string, OmnaraUIMessage>()
  let configChangeCount = 0

  for (const event of [...events].sort(
    (a, b) => sequenceNumber(a.sequence) - sequenceNumber(b.sequence),
  )) {
    if (isControlEvent(event)) continue

    if (isConfigChangeEvent(event)) {
      const action = configChangeCount === 0 && !hasOlderEvents ? 'initialized' : 'changed'
      configChangeCount += 1
      messages.push({
        id: event.id,
        role: 'assistant',
        metadata: eventMetadata(event),
        parts: [{ type: 'data-agent-config', id: `${event.id}:config`, data: { action } }],
      })
      continue
    }

    if (event.event_kind === 'agent_input') {
      const parts = agentInputParts(event)
      if (parts.length === 0) continue
      messages.push({
        id: event.id,
        role: 'user',
        metadata: eventMetadata(event),
        parts,
      })
      continue
    }

    if (event.event_kind === 'model_output') {
      const parts = modelOutputParts(event)
      let message = assistantByTurn.get(event.turn_id)
      if (message == null) {
        message = {
          id: `turn:${event.turn_id}`,
          role: 'assistant',
          metadata: eventMetadata(event),
          parts,
        }
        assistantByTurn.set(event.turn_id, message)
        messages.push(message)
      } else {
        if (message.parts.length > 0) {
          message.parts.push({ type: 'step-start', id: `${event.id}:step-start` })
        }
        message.parts.push(...parts)
        message.metadata = eventMetadata(event)
      }
      continue
    }

    if (event.event_kind === 'context_checkpoint') continue

    const message = assistantByTurn.get(event.turn_id)
    if (message == null) continue
    const index = message.parts.findIndex(
      (part) => part.type === 'dynamic-tool' && part.toolCallId === event.tool_call_id,
    )
    if (index < 0) continue
    const part = message.parts[index]
    if (part?.type !== 'dynamic-tool') continue
    const contentBlocks = event.content_blocks.filter((block) => !isHiddenContentBlock(block))
    const toolErrorCode =
      event.outcome === 'failed' && part.toolType === 'built_in'
        ? structuredToolErrorCode(contentBlocks)
        : undefined
    const toolPart: OmnaraUIMessagePart = {
      type: 'dynamic-tool',
      id: part.id,
      toolCallId: part.toolCallId,
      toolName: part.toolName,
      toolType: part.toolType,
      ...(toolErrorCode == null ? {} : { toolErrorCode }),
      state: 'output-available',
      input: part.input,
      output: {
        outcome: event.outcome,
        contentBlocks,
      },
    }
    const mediaParts = contentBlocks.flatMap((block, blockIndex) =>
      block.type === 'media_ref'
        ? [mediaPart(block, `${event.id}:block:${String(blockIndex)}`)]
        : [],
    )
    message.parts.splice(index, 1, toolPart, ...mediaParts)
    message.metadata = eventMetadata(event)
  }

  return messages
}

export function hasToolCalls(event: AgentEvent): boolean {
  return (
    event.event_kind === 'model_output' &&
    event.content_blocks.some((block) => block.type === 'tool_call')
  )
}

export function isFinishedModelOutput(event: AgentEvent): boolean {
  return event.event_kind === 'model_output' && !hasToolCalls(event)
}

export function isControlEvent(event: AgentEvent): boolean {
  return event.event_kind === 'agent_input' && event.input_kind === 'control'
}

export function isConfigChangeEvent(event: AgentEvent): boolean {
  return event.event_kind === 'agent_input' && event.input_kind === 'config_change'
}

/**
 * A turn is over once its latest event needs no further agent work. A config
 * change only ends the turn when it opens a fresh one; the backend attaches a
 * config change during an active turn to that turn instead, so it never ends it.
 */
export function isTerminalEvent(event: AgentEvent): boolean {
  if (isConfigChangeEvent(event)) return event.is_opening_event
  return isControlEvent(event) || isFinishedModelOutput(event)
}

/**
 * tool_result events are cleanup, not new work: canceling an agent appends
 * its terminal control event first and then a canceled tool_result for each
 * in-flight tool call, so the raw last event in the log is often one of
 * those trailing results. Status must be read from the latest event that
 * actually says something about turn state, skipping past that cleanup.
 */
function lastStatusEvent(events: AgentEvent[]): AgentEvent | undefined {
  for (let index = events.length - 1; index >= 0; index -= 1) {
    const candidate = events[index]
    if (
      candidate != null &&
      candidate.event_kind !== 'tool_result' &&
      candidate.event_kind !== 'context_checkpoint'
    ) {
      return candidate
    }
  }
  return undefined
}

function optimisticBacklogText(input: LocalAgentInput): string {
  if (input.text !== '') return input.text
  const filenames = input.attachments?.map((attachment) => attachment.filename).join(', ')
  return filenames == null || filenames === '' ? 'Attachment' : filenames
}

/**
 * Projects the session's raw state into what the UI renders: the durable log
 * as messages, plus the optimistic pending input, plus delta previews merged
 * into their turn's assistant message.
 *
 * `isWorking` reports whether the agent still owes work on its current turn.
 * It stays true through client-side errors — a broken stream or a failed send
 * does not stop the agent — so controls like cancel and interaction polling
 * must key off it rather than off `status`, which errors mask.
 */
export function projectAgentChat(data: AgentChatData): {
  messages: OmnaraUIMessage[]
  backlogInputs: AgentInputBacklogItem[]
  status: AgentChatStatus
  isWorking: boolean
} {
  const messages = agentEventsToMessages(data.events, { hasOlderEvents: data.hasOlderEvents })
  const deltaPreviews = deltaPreviewsByTurn(data.deltas)
  const lastEvent = lastStatusEvent(data.events)
  const isWorking = deltaPreviews.size > 0 || (lastEvent != null && !isTerminalEvent(lastEvent))
  const conversationInputIDs = new Set<string>()
  const conversationInputKeys = new Set<string>()
  for (const event of data.events) {
    if (event.event_kind !== 'agent_input') continue
    conversationInputIDs.add(event.agent_input_id)
    if (event.input_idempotency_key != null) conversationInputKeys.add(event.input_idempotency_key)
  }
  const backlogByID = new Map<string, AgentInput>()
  const backlogByKey = new Map<string, AgentInput>()
  for (const input of data.backlogInputs) {
    if (conversationInputIDs.has(input.id) || backlogByID.has(input.id)) continue
    backlogByID.set(input.id, input)
    if (input.input_idempotency_key != null) backlogByKey.set(input.input_idempotency_key, input)
  }
  const firstBacklogInputID = backlogByID.keys().next().value
  const hiddenBacklogInputIDs = new Set<string>()
  const optimisticBacklogInputs: AgentInputBacklogItem[] = []
  let localMessageCount = 0
  for (const localInput of data.localInputs) {
    if (
      conversationInputKeys.has(localInput.id) ||
      (localInput.agentInputID != null && conversationInputIDs.has(localInput.agentInputID))
    ) {
      continue
    }
    const backlogInput =
      (localInput.agentInputID == null ? undefined : backlogByID.get(localInput.agentInputID)) ??
      backlogByKey.get(localInput.id)
    const serverRequiresBacklog =
      backlogInput != null && (isWorking || firstBacklogInputID !== backlogInput.id)
    if (localInput.placement === 'backlog' || serverRequiresBacklog) {
      if (backlogInput == null) {
        optimisticBacklogInputs.push({
          id: localInput.id,
          delivery_mode: 'optimistic',
          text: optimisticBacklogText(localInput),
          attachmentCount: localInput.attachmentCount ?? localInput.attachments?.length ?? 0,
        })
      }
      continue
    }
    if (backlogInput != null) hiddenBacklogInputIDs.add(backlogInput.id)
    localMessageCount += 1
    const parts: OmnaraUIMessage['parts'] = []
    if (localInput.text !== '') {
      parts.push({
        type: 'text',
        id: `local:${localInput.id}:text`,
        text: localInput.text,
      })
    }
    for (const [index, attachment] of (localInput.attachments ?? []).entries()) {
      parts.push({
        type: 'data-media',
        id: `local:${localInput.id}:media:${String(index)}`,
        data: attachment,
      })
    }
    messages.push({
      id: `local:${localInput.id}`,
      role: 'user',
      parts,
    })
  }
  const backlogInputs: AgentInputBacklogItem[] = [
    ...[...backlogByID.values()].filter((input) => !hiddenBacklogInputIDs.has(input.id)),
    ...optimisticBacklogInputs,
  ]

  for (const [turnID, parts] of deltaPreviews) {
    const id = `turn:${turnID}`
    const index = messages.findIndex((message) => message.id === id)
    const existing = messages[index]
    if (existing != null) {
      messages[index] = { ...existing, parts: [...existing.parts, ...parts] }
    } else {
      messages.push({ id, role: 'assistant', parts })
    }
  }

  let status: AgentChatStatus = 'ready'
  if (data.error != null) status = 'error'
  else if (localMessageCount > 0) status = 'submitted'
  else if (isWorking) status = 'streaming'
  return { messages, backlogInputs, status, isWorking }
}

function deltaPreviewsByTurn(deltas: ModelOutputDelta[]): Map<string, OmnaraUIMessage['parts']> {
  const byCall = new Map<string, ModelOutputDelta[]>()
  for (const delta of deltas) {
    const call = byCall.get(delta.model_call_context_id)
    if (call == null) byCall.set(delta.model_call_context_id, [delta])
    else call.push(delta)
  }
  const byTurn = new Map<string, OmnaraUIMessage['parts']>()
  for (const [contextID, callDeltas] of byCall) {
    const ordered = [...callDeltas].sort((a, b) => a.seq - b.seq)
    const first = ordered[0]
    // Deltas are trusted all-or-nothing: a call is previewed only when its
    // frames were observed complete from the first sequence, so the preview
    // is faithful on its own. A stream joined mid-call — or one that lost a
    // frame anywhere in the pipeline — waits for the durable event instead.
    if (first == null || !isCompleteFrameRun(ordered)) continue
    const parts = modelCallPreviewParts(contextID, ordered)
    if (parts.length === 0) continue
    byTurn.set(first.turn_id, [...(byTurn.get(first.turn_id) ?? []), ...parts])
  }
  return byTurn
}

/**
 * Reports whether the seq-ordered frames carry the model call's stream in
 * full. Delta delivery is lossy at several layers — a failed publish skips a
 * frame seq, while a full producer buffer skips source seqs without skipping
 * frame seqs — so both the frame chain and the source ranges must be
 * contiguous from 1 for the preview to be trusted.
 */
function isCompleteFrameRun(deltas: ModelOutputDelta[]): boolean {
  let seq = 0
  let sourceSeq = 0
  for (const delta of deltas) {
    if (delta.seq !== seq + 1 || delta.source_seq_start !== sourceSeq + 1) return false
    if (delta.coalesced_count !== delta.source_seq_end - delta.source_seq_start + 1) return false
    seq = delta.seq
    sourceSeq = Math.max(delta.source_seq_start, delta.source_seq_end)
  }
  return deltas.length > 0
}

/** Replays one accepted model call's delta frames into preview parts. */
function modelCallPreviewParts(
  contextID: string,
  deltas: ModelOutputDelta[],
): OmnaraUIMessage['parts'] {
  const parts: OmnaraUIMessage['parts'] = []
  const blocks = new Map<number, { kind: 'text' | 'reasoning'; partIndex: number }>()
  const tools = new Map<number, { partIndex: number; inputText: string }>()

  const openTextBlock = (blockIndex: number, kind: 'text' | 'reasoning') => {
    const existing = blocks.get(blockIndex)
    if (existing != null) return existing
    const block = { kind, partIndex: parts.length }
    blocks.set(blockIndex, block)
    if (kind === 'text') {
      parts.push({
        type: 'text',
        id: `${contextID}:block:${String(blockIndex)}`,
        text: '',
        state: 'streaming',
      })
    } else {
      // An empty thinking block renders as an ephemeral placeholder until
      // visible reasoning text replaces it.
      parts.push({
        type: 'data-thinking',
        id: `${contextID}:block:${String(blockIndex)}`,
        data: { active: true },
      })
    }
    return block
  }

  for (const delta of deltas) {
    const { event } = delta
    const blockIndex = 'block_index' in event ? event.block_index : 0
    if (event.kind === 'block_start') {
      const { block } = event
      if (block.kind === 'text') openTextBlock(blockIndex, 'text')
      if (block.kind === 'thinking') openTextBlock(blockIndex, 'reasoning')
      if (block.kind === 'tool_use' && !tools.has(blockIndex)) {
        tools.set(blockIndex, { partIndex: parts.length, inputText: '' })
        parts.push({
          type: 'dynamic-tool',
          id: `${contextID}:block:${String(blockIndex)}`,
          toolCallId: block.tool_call_id,
          toolName: block.tool_name,
          state: 'input-streaming',
        })
      }
      continue
    }
    if (event.kind === 'text_delta') {
      const block = openTextBlock(blockIndex, 'text')
      const part = parts[block.partIndex]
      if (part?.type === 'text') {
        parts[block.partIndex] = { ...part, text: part.text + event.delta }
      }
      continue
    }
    if (event.kind === 'thinking_delta') {
      const block = openTextBlock(blockIndex, 'reasoning')
      const part = parts[block.partIndex]
      if (part?.type === 'data-thinking') {
        parts[block.partIndex] = {
          type: 'reasoning',
          id: part.id,
          text: event.delta,
          state: 'streaming',
        }
      } else if (part?.type === 'reasoning') {
        parts[block.partIndex] = { ...part, text: part.text + event.delta }
      }
      continue
    }
    if (event.kind === 'tool_arguments_delta') {
      const tool = tools.get(blockIndex)
      if (tool == null) continue
      tool.inputText += event.delta
      const part = parts[tool.partIndex]
      if (part?.type === 'dynamic-tool' && part.state === 'input-streaming') {
        try {
          parts[tool.partIndex] = { ...part, input: JSON.parse(tool.inputText) as unknown }
        } catch {
          // Partial JSON; the part updates once the input parses or the
          // durable event provides it.
        }
      }
      continue
    }
    if (event.kind === 'block_stop') {
      const block = blocks.get(blockIndex)
      if (block == null) continue
      const part = parts[block.partIndex]
      if (part?.type === 'data-thinking') {
        parts[block.partIndex] = { ...part, data: { active: false } }
      }
      if (part?.type === 'text' || part?.type === 'reasoning') {
        parts[block.partIndex] = { ...part, state: 'done' }
      }
    }
  }
  return parts
}
