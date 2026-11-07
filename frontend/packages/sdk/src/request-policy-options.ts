/** One SDK call, including all transport attempts; streamed body consumption is excluded. */
export interface RequestObservation {
  /** Known API method and route template, or 'unknown'; never a resolved URL. */
  operation: string
  durationMs: number
  attempts: number
  /** Time actually spent waiting between attempts, including interrupted waits. */
  backoffMs: number
  /** SDK promise settlement. A non-throwing HTTP error is 'returned'; inspect status too. */
  outcome: 'returned' | 'threw' | 'canceled' | 'timeout'
  status?: number
}

/** Transport recovery and total request budgets for clients and individual calls. */
export interface RequestPolicyOptions {
  /** Retries after the initial attempt. Factory clients default to 2, the shared client to 0. */
  maxRetries?: number
  /** Total deadline including auth and waits; raw streams end it at headers. 0 disables it. */
  timeoutMs?: number
  /** Best-effort local diagnostics. Keep callbacks cheap; they are not awaited. */
  onRequestComplete?: (observation: RequestObservation) => void | Promise<void>
}
