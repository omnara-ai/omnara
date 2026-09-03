import * as z from 'zod'

import type { AgentConfigErrorIssue, Error as ApiErrorBody } from './generated/types.gen'
import { zAgentConfigErrorIssue } from './generated/zod.gen'

export type ApiErrorCode = ApiErrorBody['code']

const errorIssuesSchema = z.object({ issues: z.array(zAgentConfigErrorIssue).optional() })

function issuesFromBody(body: unknown): AgentConfigErrorIssue[] {
  const parsed = errorIssuesSchema.safeParse(body)
  return parsed.success ? (parsed.data.issues ?? []) : []
}

export class ApiError extends Error {
  readonly status: number
  readonly code: ApiErrorCode | undefined
  readonly body: unknown
  readonly issues: AgentConfigErrorIssue[]

  constructor(status: number, message: string, code?: ApiErrorCode, body?: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.body = body
    this.issues = issuesFromBody(body)
  }

  static async fromResponse(response: Response): Promise<ApiError> {
    let body: unknown
    try {
      body = await response.clone().json()
    } catch {
      body = undefined
    }
    return ApiError.fromBody(response.status, body)
  }

  static fromBody(status: number, body: unknown): ApiError {
    let message = `request failed with status ${status}`
    let code: ApiErrorCode | undefined
    if (body && typeof body === 'object') {
      if ('error_description' in body && typeof body.error_description === 'string') {
        message = body.error_description
      } else if ('error' in body && typeof body.error === 'string') {
        message = body.error
      }
      if ('code' in body && typeof body.code === 'string') {
        code = body.code as ApiErrorCode
      }
    }
    return new ApiError(status, message, code, body)
  }
}
