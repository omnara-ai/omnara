import type * as OmnaraSDK from '@omnara/sdk'
import {
  type AgentEvent,
  AgentEventStreamError,
  type AgentInputEvent,
  type ModelOutputEvent,
  type ModelOutputStreamDelta,
  type OmnaraClient,
  type ToolResultEvent,
} from '@omnara/sdk'
import { expect, vi } from 'vitest'

const sdkMocks = vi.hoisted(() => ({
  createAgentInput: vi.fn(),
  openAgentEventStream: vi.fn(),
}))

vi.mock('@omnara/sdk', async (importOriginal) => {
  const actual = await importOriginal<typeof OmnaraSDK>()
  return {
    ...actual,
    openAgentEventStream: sdkMocks.openAgentEventStream,
    sdk: {
      ...actual.sdk,
      createAgentInput: sdkMocks.createAgentInput,
    },
  }
})

export function chatSdkMocks() {
  return sdkMocks
}

import { QueryClient } from '@tanstack/react-query'

import { AgentChatSession } from './agent-chat'
import {
  type ModelOutputDelta,
  type OmnaraUIMessage,
  projectAgentChat,
  sequenceNumber,
} from './agent-chat-messages'
import type { AgentChatStatus } from './agent-chat-types'

export const scope = { orgID: 'org', projectID: 'project', agentID: 'agent' }

export function client(statuses: number[] = [200]): OmnaraClient {
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
  data: unknown
}

export class FakeStream {
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
    reconnectDelayMs: 1,
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
  const args = sdkMocks.createAgentInput.mock.calls[call]?.[0] as
    | { headers?: { 'Idempotency-Key'?: string } }
    | undefined
  const key = args?.headers?.['Idempotency-Key']
  if (key == null) throw new Error(`no createAgentInput call ${call} recorded`)
  return key
}

export function resetChatTestHarness(): void {
  vi.clearAllMocks()
  connections.length = 0
  installStreaming()
  sdkMocks.createAgentInput.mockResolvedValue({
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
