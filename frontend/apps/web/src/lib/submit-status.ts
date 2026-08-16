import { ApiError } from '@omnara/sdk'

/**
 * Lifecycle of a one-shot submit action. The phases are mutually exclusive,
 * so components hold one SubmitStatus instead of separate submitting /
 * submitted / error variables. Components whose submit runs through a
 * TanStack mutation should keep using `mutation.isPending` for the
 * in-flight phase and only reach for this when they need a success or
 * error phase alongside it.
 */
export type SubmitStatus =
  | { phase: 'idle' }
  | { phase: 'submitting' }
  | { phase: 'success' }
  | { phase: 'error'; message: string }

export const idle: SubmitStatus = { phase: 'idle' }
export const submitting: SubmitStatus = { phase: 'submitting' }
export const success: SubmitStatus = { phase: 'success' }

export type SubmissionResult<T> = { ok: true; value: T } | { ok: false; error: unknown }

export async function settleSubmission<T>(submit: () => Promise<T>): Promise<SubmissionResult<T>> {
  try {
    return { ok: true, value: await submit() }
  } catch (error) {
    return { ok: false, error }
  }
}

export function submitError(err: unknown, fallback: string): SubmitStatus {
  return { phase: 'error', message: errorMessage(err, fallback) }
}

export function statusError(status: SubmitStatus): string | null {
  return status.phase === 'error' ? status.message : null
}

export function errorMessage(err: unknown, fallback: string): string {
  return err instanceof ApiError ? err.message : fallback
}
