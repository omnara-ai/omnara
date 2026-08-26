import {
  type AgentEvent,
  type AgentEventStreamConnectionState,
  type AgentInput,
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
  type LocalAgentInput,
  type ModelOutputDelta,
  type OmnaraUIMessage,
  parseStreamData,
  projectAgentChat,
  sequenceNumber,
} from './agent-chat-messages'
import { createAgentChatInput, isDefiniteSendFailure } from './agent-chat-transport'
import {
  type AgentInputBacklogControls,
  agentInputBacklogQueryKey,
  cacheAgentInputBacklog,
  useAgentInputBacklog,
} from './agent-input-backlog'
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
  inputBacklog: AgentInputBacklogControls
}

const historyPageSize = 100
const emptyBacklogInputs: AgentInput[] = []

function agentChatHistoryQueryKey(scope: AgentChatScope) {
  return ['agent-chat-history', scope.orgID, scope.projectID, scope.agentID]
}

export interface AgentChatSessionOptions extends AgentChatScope {
  client: OmnaraClient
  queryClient: QueryClient
  inputReconciliationDelayMs?: number
}

export class AgentChatSession {
  private readonly client: OmnaraClient
  private readonly scope: AgentChatScope
  private readonly queryClient: QueryClient
  private readonly inputReconciliationDelayMs: number

  private listeners: (() => void)[] = []
  private runController: AbortController | null = null

  private events: AgentEvent[] = []
  private deltas: ModelOutputDelta[] = []
  private completedCalls = new Set<string>()
  private cursor: number | undefined
  private localInputs = new Map<string, LocalAgentInput>()
  private lastFailedSend: { id: string; text: string } | null = null
  private error: Error | undefined
  private errorSource: 'send' | 'stream' | undefined

  private data: AgentChatData = {
    events: [],
    deltas: [],
    localInputs: [],
    backlogInputs: [],
    error: undefined,
    hasOlderEvents: false,
  }

  constructor({
    client,
    queryClient,
    orgID,
    projectID,
    agentID,
    inputReconciliationDelayMs = 1000,
  }: AgentChatSessionOptions) {
    this.client = client
    this.scope = { orgID, projectID, agentID }
    this.queryClient = queryClient
    this.inputReconciliationDelayMs = inputReconciliationDelayMs
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

  sendMessage = async (
    message: { text: string },
    placement: LocalAgentInput['placement'] = 'conversation',
  ): Promise<void> => {
    const text = message.text.trim()
    if (text === '') throw new Error('Agent messages must contain text')
    this.connect()
    const id = this.lastFailedSend?.text === text ? this.lastFailedSend.id : crypto.randomUUID()
    this.lastFailedSend = null
    this.error = undefined
    this.errorSource = undefined
    this.localInputs.set(id, { id, text, placement })
    this.notify()
    try {
      const input = await createAgentChatInput(this.client, this.scope, id, text)
      this.acceptAgentInput(id, input)
    } catch (error) {
      if (this.inputEchoLoaded(id)) {
        this.clearLocalInput(id)
        return
      }
      const sendError = error instanceof Error ? error : new Error('Could not send message')
      if (!isDefiniteSendFailure(error)) {
        void this.queryClient.invalidateQueries({
          queryKey: agentInputBacklogQueryKey(this.client, this.scope),
        })
        const signal = this.runController?.signal
        await new Promise((resolve) =>
          globalThis.setTimeout(resolve, this.inputReconciliationDelayMs),
        )
        if (signal?.aborted && this.listeners.length === 0) return
        if (this.inputEchoLoaded(id)) {
          this.clearLocalInput(id)
          return
        }
        if (this.localInputs.get(id)?.agentInputID != null) return
        this.lastFailedSend = { id, text }
      }
      this.localInputs.delete(id)
      this.error = sendError
      this.errorSource = 'send'
      this.notify()
      throw error
    }
  }

  private acceptAgentInput(id: string, input: AgentInput): void {
    if (this.inputEchoLoaded(id)) {
      this.clearLocalInput(id)
    } else {
      const localInput = this.localInputs.get(id)
      if (localInput != null) {
        this.localInputs.set(id, { ...localInput, agentInputID: input.id })
        cacheAgentInputBacklog(this.queryClient, this.client, this.scope, input)
        void this.queryClient.invalidateQueries({
          queryKey: agentInputBacklogQueryKey(this.client, this.scope),
        })
        this.notify()
      }
    }
    void this.queryClient.invalidateQueries({
      predicate: projectActorsQueryPredicate(this.scope.orgID, this.scope.projectID),
    })
  }

  private clearLocalInput(id: string): void {
    if (!this.localInputs.delete(id)) return
    this.notify()
  }

  beginBacklogInputCancellation = (inputIDs: string[]): (() => void) => {
    const ids = new Set(inputIDs)
    const dismissed = [...this.localInputs].filter(([, input]) => ids.has(input.agentInputID ?? ''))
    dismissed.forEach(([id]) => this.localInputs.delete(id))
    if (dismissed.length > 0) this.notify()
    return () => {
      const restored = dismissed.filter(([, input]) => !this.inputEchoLoaded(input.id))
      restored.forEach(([id, input]) => this.localInputs.set(id, input))
      if (restored.length > 0) this.notify()
    }
  }

  confirmBacklogInputs = (inputs: AgentInput[]): void => {
    const byID = new Map(inputs.map((input) => [input.id, input]))
    const byKey = new Map(
      inputs.flatMap((input) =>
        input.input_idempotency_key == null ? [] : [[input.input_idempotency_key, input] as const],
      ),
    )
    let changed = false
    for (const [id, localInput] of this.localInputs) {
      const backlogInput =
        (localInput.agentInputID == null ? undefined : byID.get(localInput.agentInputID)) ??
        byKey.get(id)
      if (backlogInput == null) continue
      if (localInput.agentInputID !== backlogInput.id) {
        this.localInputs.set(id, { ...localInput, agentInputID: backlogInput.id })
        changed = true
      }
    }
    if (changed) this.notify()
  }

  private inputEchoLoaded(id: string): boolean {
    const matches = (event: AgentEvent) =>
      event.event_kind === 'agent_input' && event.input_idempotency_key === id
    if (this.events.some(matches)) return true
    const history = this.queryClient.getQueryData<InfiniteData<{ data: AgentEvent[] }>>(
      agentChatHistoryQueryKey(this.scope),
    )
    return history?.pages.some((page) => page.data.some(matches)) ?? false
  }

  private notify(): void {
    const localInputs = [...this.localInputs.values()]
    this.data = {
      events: this.events,
      deltas: this.deltas,
      localInputs,
      backlogInputs: [],
      error: this.error,
      hasOlderEvents: false,
    }
    for (const listener of this.listeners) listener()
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

    const inputIdempotencyKey =
      event.event_kind === 'agent_input' ? event.input_idempotency_key : undefined
    let localInputEcho =
      inputIdempotencyKey != null && inputIdempotencyKey === this.lastFailedSend?.id
    if (localInputEcho) {
      this.lastFailedSend = null
      if (this.errorSource === 'send') {
        this.error = undefined
        this.errorSource = undefined
      }
    }
    if (event.event_kind === 'agent_input') {
      for (const [id, input] of this.localInputs) {
        if (id !== inputIdempotencyKey && input.agentInputID !== event.agent_input_id) continue
        this.localInputs.delete(id)
        localInputEcho = true
      }
    }
    if (localInputEcho) {
      void this.queryClient.invalidateQueries({
        predicate: projectActorsQueryPredicate(this.scope.orgID, this.scope.projectID),
      })
    }
    if (event.event_kind === 'agent_input' && event.input_kind === 'content') {
      void this.queryClient.invalidateQueries({
        queryKey: agentInputBacklogQueryKey(this.client, this.scope),
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

  private handleConnectionState(state: AgentEventStreamConnectionState): void {
    if (state.state === 'reconnecting') {
      this.deltas = []
      this.notify()
      return
    }
    if (state.reconnected) {
      void this.queryClient.invalidateQueries({
        queryKey: openAgentInteractionsQueryKey(this.client, this.scope),
      })
    }
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
    const cursor = this.cursor
    if (cursor == null) return
    try {
      const stream = openAgentEventStream({
        client: this.client,
        path: this.scope,
        query: { after_sequence: cursor, stream_deltas: true },
        signal,
        onConnectionStateChange: (state) => {
          this.handleConnectionState(state)
        },
      })
      for await (const data of stream) {
        const parsed = parseStreamData(data)
        if (parsed.kind === 'delta') this.handleDelta(parsed.delta)
        else if (parsed.kind === 'event') this.handleEvent(parsed.event)
      }
    } catch (error) {
      if (signal.aborted) return
      const message = error instanceof Error ? error.message : 'Agent event stream failed'
      this.handleStreamError(message)
      this.disconnect()
    }
  }
}

export function useAgentChat(scope: AgentChatScope): UseAgentChatResult {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  const { orgID, projectID, agentID } = scope
  const inputBacklog = useAgentInputBacklog(scope)

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
  const authoritativeBacklogInputs = inputBacklog.query.data?.data ?? emptyBacklogInputs
  useEffect(() => {
    session.confirmBacklogInputs(authoritativeBacklogInputs)
  }, [authoritativeBacklogInputs, session])
  const hasOlderEvents = history.status !== 'success' || history.hasNextPage
  const data = useMemo(
    () => ({
      ...sessionData,
      events: [...(history.data?.events ?? []), ...sessionData.events],
      backlogInputs: authoritativeBacklogInputs,
      hasOlderEvents,
    }),
    [authoritativeBacklogInputs, history.data, hasOlderEvents, sessionData],
  )
  const projected = useMemo(() => projectAgentChat(data), [data])
  const inputPlacement =
    projected.isWorking || projected.backlogInputs.length > 0 || sessionData.localInputs.length > 0
      ? 'backlog'
      : 'conversation'

  return {
    messages: projected.messages,
    status: projected.status,
    isWorking: projected.isWorking,
    error: data.error,
    historyStatus: history.status,
    hasOlderMessages: history.hasNextPage,
    isLoadingOlderMessages: history.isFetchingNextPage,
    loadOlderMessages: () => void history.fetchNextPage(),
    sendMessage: (message) => session.sendMessage(message, inputPlacement),
    inputBacklog: {
      inputs: projected.backlogInputs,
      actionPending:
        inputBacklog.cancel.isPending ||
        inputBacklog.promote.isPending ||
        inputBacklog.move.isPending,
      beginCancellation: (inputIDs) =>
        inputBacklog.beginCancellation(inputIDs, () =>
          session.beginBacklogInputCancellation(inputIDs),
        ),
      cancel: async (inputID) => {
        const rollback = session.beginBacklogInputCancellation([inputID])
        try {
          return await inputBacklog.cancel.mutateAsync(inputID)
        } catch (error) {
          rollback()
          throw error
        }
      },
      promote: inputBacklog.promote.mutateAsync,
      move: inputBacklog.move.mutateAsync,
    },
  }
}
