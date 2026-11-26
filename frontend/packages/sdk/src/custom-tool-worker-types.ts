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
  /** The turn the call was made in. That turn's input events say who asked for it. */
  turnID: string
  name: string
  input: ToolInput
  /**
   * When the call's tool has a timeout: the time on this machine's clock, in epoch
   * milliseconds, by which Omnara must have the result. A later result is refused.
   */
  answerBy?: number
  signal: AbortSignal
}

export type CustomToolCallRef = Pick<CustomToolCall, 'agentID' | 'toolCallID' | 'name'>
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
  results: {
    call: CustomToolCallRef
    outcome: CustomToolOutcome | { status: 'failed'; error: Error }
  }[]
  nextCursor: string | null
}

export type WorkSource = 'notification' | 'recovery'

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
  /** Handler errors become failed tool results; request failures still reach the caller. */
  onError?: (error: Error, call?: CustomToolCallRef) => void | Promise<void>
  /** Not awaited; callback failures do not affect execution. */
  onObserve?: (observation: CustomToolObservation) => void | Promise<void>
}

export interface CustomToolWorker {
  /** Pass only authenticated SSE frames or signature-verified webhook updates. */
  handle: (
    update: ToolCallUpdate,
    options?: CustomToolOperationOptions,
  ) => Promise<CustomToolOutcome>
  check: (options?: CustomToolCheckOptions) => Promise<CustomToolCheckResult>
}
