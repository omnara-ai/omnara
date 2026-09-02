import { ApiError } from '@omnara/sdk'

const managedWorkAdmissionDeniedCode = 'managed_work_admission_denied'
const insufficientOmnaraCreditsMessage = 'Insufficient Omnara credits.'

export function isInsufficientCreditsError(cause: unknown): cause is ApiError {
  return cause instanceof ApiError && cause.code === managedWorkAdmissionDeniedCode
}

export function isInsufficientCreditsModelError(text: string): boolean {
  return text === insufficientOmnaraCreditsMessage
}

export function isInsufficientCreditsToolError(errorCode: string | undefined): boolean {
  return errorCode === managedWorkAdmissionDeniedCode
}
