import * as z from 'zod'

import type { Error as ApiErrorBody, ErrorIssue } from './generated/types.gen'
import { zErrorIssue } from './generated/zod.gen'

export type ApiErrorCode = ApiErrorBody['code']

const errorIssuesSchema = z.object({ issues: z.array(zErrorIssue).optional() })

function issuesFromBody(body: unknown): ErrorIssue[] {
  const parsed = errorIssuesSchema.safeParse(body)
  return parsed.success ? (parsed.data.issues ?? []) : []
}

export class ApiError extends Error {
  readonly status: number
  readonly code: ApiErrorCode | undefined
  readonly body: unknown
  readonly issues: ErrorIssue[]

  constructor(status: number, message: string, code?: ApiErrorCode, body?: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.body = body
    this.issues = issuesFromBody(body)
  }

  static async fromResponse(response: Response): Promise<ApiError> {
    let message = `request failed with status ${response.status}`
    let code: ApiErrorCode | undefined
    let data: unknown
    try {
      data = await response.clone().json()
    } catch {
      data = undefined
    }
    if (data && typeof data === 'object') {
      if ('error' in data && typeof data.error === 'string') {
        message = data.error
      }
      if ('code' in data && typeof data.code === 'string') {
        code = data.code as ApiErrorCode
      }
    }
    return new ApiError(response.status, message, code, data)
  }
}
