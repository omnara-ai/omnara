export interface RequestObservation {
  /** Known API method and route template, or 'unknown'; never a resolved URL. */
  operation: string
  /** Excludes streamed body consumption. */
  durationMs: number
  attempts: number
  backoffMs: number
  /** SDK promise settlement. A non-throwing HTTP error is 'returned'; inspect status too. */
  outcome: 'returned' | 'threw' | 'canceled' | 'timeout'
  status?: number
}

export interface RequestPolicyOptions {
  /** Retries after the initial attempt. Factory clients default to 2, the shared client to 0. */
  maxRetries?: number
  /** Total deadline including auth and waits; raw streams end it at headers. 0 disables it. */
  timeoutMs?: number
  /** Not awaited; callback failures do not affect the request. */
  onRequestComplete?: (observation: RequestObservation) => void | Promise<void>
}
