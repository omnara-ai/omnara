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
export interface CustomToolOutcome {
  status: 'completed' | 'deferred' | 'ignored'
}

export interface CustomToolOperationOptions {
  /** The first invocation owns cancellation. A duplicate caller only cancels its own wait. */
  signal?: AbortSignal
}

export interface CustomToolCheckOptions extends CustomToolOperationOptions {
  cursor?: string
  /** Maximum number of rows read and handled by this invocation, from 1 to 100. */
  limit?: number
}

export interface CustomToolCheckResult {
  /** Consume every failed outcome before advancing your application's recovery checkpoint. */
  results: {
    call: CustomToolCallRef
    outcome: CustomToolOutcome | { status: 'failed'; error: Error }
  }[]
  nextCursor: string | null
}

export type WorkSource = 'notification' | 'recovery'

/** Local observations contain no tool names, resource identifiers, inputs, or results. */
export type CustomToolObservation =
  | { stage: 'admission'; source: WorkSource; outcome: 'started' | 'joined' }
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
      outcome: 'accepted' | 'already_completed' | 'failed'
    }
  | { stage: 'recovery'; durationMs: number; outcome: 'page' | 'failed'; rows: number }

export interface CreateCustomToolWorkerOptions {
  client: OmnaraClient
  path: ListProjectToolCallsData['path']
  agentProfileID?: string
  tools: Record<string, CustomToolHandler>
  /** Optional diagnostics. Failures still reach handle() or the check() result. */
  onError?: (error: Error, call?: CustomToolCallRef) => void | Promise<void>
  /** Record inexpensive local metrics; observer failures never affect execution. */
  onObserve?: (observation: CustomToolObservation) => void | Promise<void>
}

export interface CustomToolWorker {
  /** Pass only authenticated SSE frames or signature-verified webhook updates. */
  handle: (
    update: ToolCallUpdate,
    options?: CustomToolOperationOptions,
  ) => Promise<CustomToolOutcome>
  /** Read and await one bounded page. The application owns subsequent checks and recovery. */
  check: (options?: CustomToolCheckOptions) => Promise<CustomToolCheckResult>
}
