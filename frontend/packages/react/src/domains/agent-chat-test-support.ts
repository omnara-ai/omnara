import {
  type AgentEvent,
  type AgentEventStreamConnectionState,
  type AgentEventStreamFrame,
  type AgentInputEvent,
  createOmnaraClient,
  type ModelOutputEvent,
  type ModelOutputStreamDelta,
  type OmnaraClient,
  type ToolResultEvent,
} from '@omnara/sdk'
import { QueryClient } from '@tanstack/react-query'
import { expect, vi } from 'vitest'

import { AgentChatSession } from './agent-chat'
import {
  type ModelOutputDelta,
  type OmnaraUIMessage,
  projectAgentChat,
  sequenceNumber,
} from './agent-chat-messages'
import type { AgentChatStatus, AgentChatTransport } from './agent-chat-types'

const transport = {
  createAgentInput: vi.fn<AgentChatTransport['createAgentInput']>(),
  openAgentEventStream: vi.fn<AgentChatTransport['openAgentEventStream']>(),
}

export function chatTransport() {
  return transport
}

export const scope = { orgID: 'org', projectID: 'project', agentID: 'agent' }

export function client(): OmnaraClient {
  return createOmnaraClient({ baseUrl: 'http://localhost' })
}

export function event(overrides: Partial<ModelOutputEvent> = {}): ModelOutputEvent {
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

export function userInputEvent(overrides: Partial<AgentInputEvent> = {}): AgentInputEvent {
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

export function toolResultEvent(overrides: Partial<ToolResultEvent> = {}): ToolResultEvent {
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

export function controlEvent(overrides: Partial<AgentInputEvent> = {}): AgentInputEvent {
  return userInputEvent({
    id: 'cancel-event',
    content_blocks: [],
    input_kind: 'control',
    control_type: 'cancel_current',
    ...overrides,
  })
}

export function configChangeEvent(overrides: Partial<AgentInputEvent> = {}): AgentInputEvent {
  return userInputEvent({
    id: 'config-event',
    content_blocks: [],
    input_kind: 'config_change',
    ...overrides,
  })
}

export function toolCallBlock(toolCallID = 'call') {
  const input = { command: 'pwd' }
  return {
    type: 'tool_call' as const,
    tool_call_id: toolCallID,
    tool_type: 'built_in' as const,
    name: 'shell',
    input,
  }
}

export function delta(
  seq: number,
  deltaEvent: ModelOutputStreamDelta,
  overrides: Partial<ModelOutputDelta> = {},
): ModelOutputDelta {
  const frame = {
    turn_id: 'turn',
    model_call_context_id: 'mcc',
    seq,
    source_seq_start: seq,
    source_seq_end: seq,
    event: deltaEvent,
    ...overrides,
  }
  return {
    coalesced_count: frame.source_seq_end - frame.source_seq_start + 1,
    ...frame,
  }
}

interface Frame {
  event: string
  data: AgentEventStreamFrame
}

export class FakeStream {
  private queue: Frame[] = []
  private wake: (() => void) | null = null
  private ended = false
  private failure: Error | undefined
  private onConnectionStateChange: ((state: AgentEventStreamConnectionState) => void) | undefined

  constructor(onConnectionStateChange?: (state: AgentEventStreamConnectionState) => void) {
    this.onConnectionStateChange = onConnectionStateChange
  }

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

  connectionState(state: AgentEventStreamConnectionState): void {
    this.onConnectionStateChange?.(state)
  }

  async *drive(open: () => boolean | Promise<boolean>): AsyncGenerator<AgentEventStreamFrame> {
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
  transport.openAgentEventStream.mockImplementation((options) => {
    const stream = new FakeStream(options.onConnectionStateChange)
    connections.push(stream)
    options.signal?.addEventListener('abort', () => {
      stream.end()
    })
    const open = () => {
      stream.connectionState({ state: 'connected', reconnected: false })
      return true
    }
    return stream.drive(open)
  })
}

export async function connection(index: number): Promise<FakeStream> {
  await vi.waitFor(() => {
    expect(connections.length).toBeGreaterThan(index)
  })
  const stream = connections[index]
  if (stream == null) throw new Error(`missing connection ${index}`)
  return stream
}

export interface ChatState {
  messages: OmnaraUIMessage[]
  status: AgentChatStatus
  error: Error | undefined
}

const sessionHistory = new WeakMap<AgentChatSession, AgentEvent[]>()
const sessionHasOlderEvents = new WeakMap<AgentChatSession, boolean>()

export function read(session: AgentChatSession): ChatState {
  const sessionData = session.getData()
  const data = {
    ...sessionData,
    events: [...(sessionHistory.get(session) ?? []), ...sessionData.events],
    hasOlderEvents: sessionHasOlderEvents.get(session) ?? false,
  }
  const { messages, status } = projectAgentChat(data)
  return { messages, status, error: data.error }
}

export function startSession(
  history: AgentEvent[] = [],
  sessionClient = client(),
  queryClient = new QueryClient(),
  hasOlderEvents = false,
): AgentChatSession {
  const session = createChatTestSession(sessionClient, queryClient)
  sessionHistory.set(session, history)
  sessionHasOlderEvents.set(session, hasOlderEvents)
  session.start(sequenceNumber(history.at(-1)?.sequence))
  session.subscribe(() => undefined)
  return session
}

export function createChatTestSession(
  sessionClient = client(),
  queryClient = new QueryClient(),
): AgentChatSession {
  return new AgentChatSession({
    client: sessionClient,
    queryClient,
    ...scope,
    inputReconciliationDelayMs: 1,
    transport,
  })
}

export async function waitForSnapshot(
  session: AgentChatSession,
  predicate: (state: ChatState) => boolean,
): Promise<ChatState> {
  await vi.waitFor(() => {
    expect(predicate(read(session))).toBe(true)
  })
  return read(session)
}

export function messageText(message: OmnaraUIMessage | undefined): string[] {
  return message?.parts.flatMap((part) => (part.type === 'text' ? [part.text] : [])) ?? []
}

export function sentIdempotencyKey(call = 0): string {
  const key = transport.createAgentInput.mock.calls[call]?.[0].headers?.['Idempotency-Key']
  if (key == null) throw new Error(`no createAgentInput call ${call} recorded`)
  return key
}

export function resetChatTestHarness(): void {
  vi.clearAllMocks()
  connections.length = 0
  installStreaming()
  transport.createAgentInput.mockResolvedValue({
    data: {
      agent_input: {
        id: 'input-1',
        agent_id: 'agent',
        state: 'received',
        delivery_mode: 'queued',
        input_kind: 'content',
        content_blocks: [{ type: 'text', text: 'Hello' }],
        queued_at: '2026-07-14T00:00:00Z',
      },
    },
  })
}
