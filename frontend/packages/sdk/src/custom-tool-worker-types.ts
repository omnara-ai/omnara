import type { OmnaraClient } from './client'
import type {
  ListProjectToolCallsData,
  SubmitToolCallResultRequest,
  ToolCallUpdate,
  ToolInput,
} from './generated/types.gen'

export interface CustomToolCall {
  agentID: string
  toolCallID: string
  name: string
  input: ToolInput
  signal: AbortSignal
}

export type CustomToolCallRef = Omit<CustomToolCall, 'input' | 'signal'>
export type CustomToolHandlerResult = SubmitToolCallResultRequest | { outcome: 'deferred' }
export type CustomToolHandler = (call: CustomToolCall) => Promise<CustomToolHandlerResult>

export type CustomToolOutcome =
  | { status: 'completed' | 'deferred' | 'ignored' | 'busy' | 'stopped' }
  | { status: 'failed'; error: Error }

export interface CustomToolOperationOptions {
  signal?: AbortSignal
}

export interface CustomToolCheckOptions extends CustomToolOperationOptions {
  cursor?: string
  limit?: number
}

export interface CustomToolCheckResult {
  results: { call: CustomToolCallRef; outcome: CustomToolOutcome }[]
  nextCursor: string | null
  /** Retained results blocked the next read. Resume at nextCursor (or the beginning if null). */
  blocked: boolean
}

export type WorkSource = 'notification' | 'recovery' | 'retained'

/** Local observations contain no tool names, resource identifiers, inputs, or results. */
export type CustomToolObservation =
  | { stage: 'capacity'; running: number; retained: number; reserved: number }
  | {
      stage: 'admission'
      source: WorkSource
      outcome: 'started' | 'joined' | 'settled' | 'busy' | 'stopped'
    }
  | {
      stage: 'readiness'
      durationMs: number
      outcome: 'ready' | 'completed' | 'ignored' | 'failed'
    }
  | {
      stage: 'handler'
      source: WorkSource
      durationMs: number
      outcome: 'succeeded' | 'failed' | 'deferred' | 'canceled'
    }
  | {
      stage: 'submission'
      durationMs: number
      retainedForMs: number
      outcome: 'accepted' | 'already_completed' | 'not_ready' | 'rejected' | 'failed'
    }
  | {
      stage: 'recovery'
      durationMs: number
      waitMs: number
      outcome: 'page' | 'failed' | 'blocked'
      rows: number
      started: number
    }
  | { stage: 'recovery'; outcome: 'complete'; durationMs: number }

export interface CreateCustomToolWorkerOptions {
  client: OmnaraClient
  path: ListProjectToolCallsData['path']
  agentProfileID?: string
  tools: Record<string, CustomToolHandler>
  concurrency?: number
  signal?: AbortSignal
  shutdownGraceMs?: number
  requestTimeoutMs?: number
  onError: (error: Error, call?: CustomToolCallRef) => void | Promise<void>
  /** Record inexpensive local metrics; observer failures never affect execution. */
  onObserve?: (observation: CustomToolObservation) => void | Promise<void>
}

export interface CustomToolWorker {
  /** Pass only authenticated SSE frames or signature-verified webhook updates. */
  handle: (
    update: ToolCallUpdate,
    options?: CustomToolOperationOptions,
  ) => Promise<CustomToolOutcome>
  check: (options?: CustomToolCheckOptions) => Promise<CustomToolCheckResult>
  run: () => Promise<void>
  shutdown: () => Promise<void>
}
