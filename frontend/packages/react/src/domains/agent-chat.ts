import {
  type AgentEvent,
  AgentEventStreamError,
  type Error as APIError,
  type OmnaraClient,
  openAgentEventStream,
  sdk,
} from '@omnara/sdk'
import {
  type InfiniteData,
  type QueryClient,
  type QueryStatus,
  useInfiniteQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { useEffect, useMemo, useSyncExternalStore } from 'react'

import { useOmnaraClient } from '../omnara-client'
import { projectActorsQueryPredicate } from './actors'
import {
  type AgentChatData,
  type AgentChatStatus,
  hasToolCalls,
  isControlEvent,
  isTerminalEvent,
  type ModelOutputDelta,
  type OmnaraUIMessage,
  parseStreamData,
  projectAgentChat,
  sequenceNumber,
} from './agent-chat-messages'
import { openAgentInteractionsQueryKey } from './agent-interactions'

export type {
  AgentChatData,
  AgentChatStatus,
  OmnaraMessageMetadata,
  OmnaraUIMessage,
} from './agent-chat-messages'

export type AgentChatHistoryStatus = QueryStatus

export interface AgentChatScope {
  orgID: string
  projectID: string
  agentID: string
}

export interface UseAgentChatResult {
  messages: OmnaraUIMessage[]
  status: AgentChatStatus
  isWorking: boolean
  error: Error | undefined
  historyStatus: AgentChatHistoryStatus
  hasOlderMessages: boolean
  isLoadingOlderMessages: boolean
  loadOlderMessages: () => void
  sendMessage: (message: { text: string }) => Promise<void>
}

const historyPageSize = 100
const maxReconnectDelayMs = 30_000

function reconnectBackoff(baseDelayMs: number, consecutiveFailures: number): number {
  const exponent = Math.min(Math.max(consecutiveFailures - 1, 0), 30)
  return Math.min(baseDelayMs * 2 ** exponent, maxReconnectDelayMs)
}

function agentChatHistoryQueryKey(scope: AgentChatScope) {
  return ['agent-chat-history', scope.orgID, scope.projectID, scope.agentID]
}

export interface AgentChatSessionOptions extends AgentChatScope {
  client: OmnaraClient
  queryClient: QueryClient
  reconnectDelayMs?: number
}

function abortableDelay(ms: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) return Promise.resolve()
  return new Promise((resolve) => {
    const timeout = globalThis.setTimeout(done, ms)
    function done() {
      globalThis.clearTimeout(timeout)
      signal.removeEventListener('abort', done)
      resolve()
    }
    signal.addEventListener('abort', done, { once: true })
  })
}

export class AgentChatSession {
  private readonly client: OmnaraClient
  private readonly scope: AgentChatScope
  private readonly queryClient: QueryClient
  private readonly reconnectDelayMs: number

  private listeners: (() => void)[] = []
  private runController: AbortController | null = null

  private events: AgentEvent[] = []
  private deltas: ModelOutputDelta[] = []
  private completedCalls = new Set<string>()
  private cursor: number | undefined
  private pendingInput: { id: string; text: string } | null = null
  private lastFailedSend: { id: string; text: string } | null = null
  private error: Error | undefined
  private errorSource: 'send' | 'stream' | undefined

  private data: AgentChatData = {
    events: [],
    deltas: [],
    pendingInput: null,
    error: undefined,
    hasOlderEvents: false,
  }

  constructor({
    client,
    queryClient,
    orgID,
    projectID,
    agentID,
    reconnectDelayMs = 1000,
  }: AgentChatSessionOptions) {
    this.client = client
    this.scope = { orgID, projectID, agentID }
    this.queryClient = queryClient
    this.reconnectDelayMs = reconnectDelayMs
  }

  getData = (): AgentChatData => this.data

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.push(listener)
    this.connect()
    return () => {
      this.listeners = this.listeners.filter((l) => l !== listener)
      if (this.listeners.length === 0) this.disconnect()
    }
  }

  start = (afterSequence: number): void => {
    if (this.cursor != null) return
    this.cursor = afterSequence
    this.connect()
  }

  sendMessage = async (message: { text: string }): Promise<void> => {
    const text = message.text.trim()
    if (text === '') throw new Error('Agent messages must contain text')
    this.connect()
    const id = this.lastFailedSend?.text === text ? this.lastFailedSend.id : crypto.randomUUID()
    this.error = undefined
    this.errorSource = undefined
    this.pendingInput = { id, text }
    this.notify()
    try {
      await sdk.createAgentInput({
        client: this.client,
        path: this.scope,
        headers: { 'Idempotency-Key': id },
        body: {
          content_blocks: [
            {
              type: 'text',
              text: 'This message came from the Omnara web app. Reply with normal assistant text unless explicitly asked to message an integration.',
              metadata: { omnara_hidden: 'true' },
            },
            { type: 'text', text },
          ],
        },
      })
      this.lastFailedSend = null
      if (this.inputEchoLoaded(id)) this.clearPendingInput(id)
      void this.queryClient.invalidateQueries({
        predicate: projectActorsQueryPredicate(this.scope.orgID, this.scope.projectID),
      })
    } catch (error) {
      if (this.inputEchoLoaded(id)) {
        this.clearPendingInput(id)
        return
      }
      this.pendingInput = null
      this.lastFailedSend = { id, text }
      this.error = error instanceof Error ? error : new Error('Could not send message')
      this.errorSource = 'send'
      this.notify()
      throw error
    }
  }

  private clearPendingInput(id: string): void {
    if (this.pendingInput?.id !== id) return
    this.pendingInput = null
    this.notify()
  }

  private inputEchoLoaded(id: string): boolean {
    if (
      this.events.some(
        (event) => event.event_kind === 'agent_input' && event.input_idempotency_key === id,
      )
    ) {
      return true
    }
    const history = this.queryClient.getQueryData<InfiniteData<{ data: AgentEvent[] }>>(
      agentChatHistoryQueryKey(this.scope),
    )
    return (
      history?.pages.some((page) =>
        page.data.some(
          (event) => event.event_kind === 'agent_input' && event.input_idempotency_key === id,
        ),
      ) ?? false
    )
  }

  private notify(): void {
    this.data = {
      events: this.events,
      deltas: this.deltas,
      pendingInput: this.pendingInput,
      error: this.error,
      hasOlderEvents: false,
    }
    this.listeners.map((listener) => {
      listener()
    })
  }

  private handleEvent(event: AgentEvent): void {
    const sequence = sequenceNumber(event.sequence)
    if (this.cursor == null || sequence <= this.cursor) return
    this.cursor = sequence
    this.events = [...this.events, event]
    if (this.errorSource === 'stream') {
      this.error = undefined
      this.errorSource = undefined
    }

    if (
      event.event_kind === 'agent_input' &&
      event.input_idempotency_key != null &&
      (event.input_idempotency_key === this.pendingInput?.id ||
        event.input_idempotency_key === this.lastFailedSend?.id)
    ) {
      this.pendingInput = null
      this.lastFailedSend = null
      if (this.errorSource === 'send') {
        this.error = undefined
        this.errorSource = undefined
      }
      void this.queryClient.invalidateQueries({
        predicate: projectActorsQueryPredicate(this.scope.orgID, this.scope.projectID),
      })
    }
    if (event.event_kind === 'model_output') {
      this.completedCalls.add(event.model_call_context_id)
      this.deltas = this.deltas.filter(
        (delta) => delta.model_call_context_id !== event.model_call_context_id,
      )
    }
    if (isTerminalEvent(event)) {
      this.deltas = this.deltas.filter((delta) => delta.turn_id !== event.turn_id)
    }
    if (hasToolCalls(event) || event.event_kind === 'tool_result' || isControlEvent(event)) {
      void this.queryClient.invalidateQueries({
        queryKey: openAgentInteractionsQueryKey(this.client, this.scope),
      })
    }
    this.notify()
  }

  private handleDelta(delta: ModelOutputDelta): void {
    if (this.completedCalls.has(delta.model_call_context_id)) return
    if (this.errorSource === 'stream') {
      this.error = undefined
      this.errorSource = undefined
    }
    if (delta.event.kind === 'error') {
      this.completedCalls.add(delta.model_call_context_id)
      this.deltas = this.deltas.filter(
        (candidate) => candidate.model_call_context_id !== delta.model_call_context_id,
      )
      this.notify()
      return
    }
    this.deltas = [...this.deltas, delta]
    this.notify()
  }

  private handleStreamError(message: string): void {
    this.error = new Error(message)
    this.errorSource = 'stream'
    this.notify()
  }

  disconnect = (): void => {
    this.runController?.abort()
    this.runController = null
  }

  private connect(): void {
    if (this.cursor == null || this.listeners.length === 0 || this.runController != null) return
    this.runController = new AbortController()
    void this.run(this.runController.signal)
  }

  private async run(signal: AbortSignal): Promise<void> {
    let consecutiveFailures = 0
    while (!signal.aborted) {
      const cursor = this.cursor
      if (cursor == null) return
      try {
        let streamError: APIError | undefined
        const { stream } = await openAgentEventStream({
          client: this.client,
          path: this.scope,
          query: { after_sequence: cursor, stream_deltas: true },
          signal,
        })
        for await (const data of stream) {
          const parsed = parseStreamData(data)
          if (parsed.kind === 'error') {
            streamError = parsed.error
            break
          }
          consecutiveFailures = 0
          if (parsed.kind === 'delta') this.handleDelta(parsed.delta)
          else if (parsed.kind === 'event') this.handleEvent(parsed.event)
        }
        if (streamError != null) {
          if (streamError.code !== 'service_unavailable') {
            this.handleStreamError(streamError.error)
            this.disconnect()
            return
          }
          consecutiveFailures += 1
          await abortableDelay(reconnectBackoff(this.reconnectDelayMs, consecutiveFailures), signal)
          continue
        }
      } catch (error) {
        if (error instanceof AgentEventStreamError) {
          if (error.kind === 'aborted') return
          if (error.retryable) {
            consecutiveFailures += 1
            await abortableDelay(
              reconnectBackoff(this.reconnectDelayMs, consecutiveFailures),
              signal,
            )
            continue
          }
        }
        const message = error instanceof Error ? error.message : 'Agent event stream failed'
        this.handleStreamError(message)
        this.disconnect()
        return
      }
      consecutiveFailures += 1
      await abortableDelay(reconnectBackoff(this.reconnectDelayMs, consecutiveFailures), signal)
    }
  }
}

export function useAgentChat(scope: AgentChatScope): UseAgentChatResult {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  const { orgID, projectID, agentID } = scope

  const history = useInfiniteQuery({
    queryKey: agentChatHistoryQueryKey({ orgID, projectID, agentID }),
    initialPageParam: 0,
    queryFn: async ({ pageParam, signal }) => {
      const { data: page } = await sdk.listEvents({
        client,
        path: { orgID, projectID, agentID },
        query: { before_sequence: pageParam, limit: historyPageSize },
        signal,
      })
      return page
    },
    getNextPageParam: (lastPage) => {
      const nextBeforeSequence = lastPage.next_before_sequence
      return nextBeforeSequence == null ? undefined : sequenceNumber(nextBeforeSequence)
    },
    select: (data) => ({
      ...data,
      events: [...data.pages].reverse().flatMap((page) => page.data),
    }),
    staleTime: Infinity,
  })

  const newestLoadedSequence = sequenceNumber(
    history.data?.pages[0]?.data.at(-1)?.sequence ?? undefined,
  )
  const session = useMemo(
    () =>
      new AgentChatSession({
        client,
        queryClient,
        orgID,
        projectID,
        agentID,
      }),
    [agentID, client, orgID, projectID, queryClient],
  )
  useEffect(() => {
    if (history.status === 'success') session.start(newestLoadedSequence)
  }, [history.status, newestLoadedSequence, session])
  const sessionData = useSyncExternalStore(session.subscribe, session.getData, session.getData)
  const hasOlderEvents = history.status !== 'success' || history.hasNextPage
  const data = useMemo(
    () => ({
      ...sessionData,
      events: [...(history.data?.events ?? []), ...sessionData.events],
      hasOlderEvents,
    }),
    [history.data, hasOlderEvents, sessionData],
  )
  const { messages, status, isWorking } = useMemo(() => projectAgentChat(data), [data])

  return {
    messages,
    status,
    isWorking,
    error: data.error,
    historyStatus: history.status,
    hasOlderMessages: history.hasNextPage,
    isLoadingOlderMessages: history.isFetchingNextPage,
    loadOlderMessages: () => void history.fetchNextPage(),
    sendMessage: session.sendMessage,
  }
}
