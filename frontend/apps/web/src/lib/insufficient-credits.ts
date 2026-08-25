import { ApiError } from '@omnara/sdk'

const managedWorkAdmissionDeniedCode = 'managed_work_admission_denied'
const insufficientOmnaraCreditsMessage = 'Insufficient Omnara credits.'

export function isInsufficientCreditsError(error: unknown): error is ApiError {
  return error instanceof ApiError && error.code === managedWorkAdmissionDeniedCode
}

export function isInsufficientCreditsModelError(text: string): boolean {
  return text === insufficientOmnaraCreditsMessage
}

export function isInsufficientCreditsToolError(errorCode: string | undefined): boolean {
  return errorCode === managedWorkAdmissionDeniedCode
}
