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
    let message = `request failed with status ${response.status}`
    let code: ApiErrorCode | undefined
    let data: unknown
    try {
      data = await response.clone().json()
    } catch {
      data = undefined
    }
    if (data && typeof data === 'object') {
      if ('error_description' in data && typeof data.error_description === 'string') {
        message = data.error_description
      } else if ('error' in data && typeof data.error === 'string') {
        message = data.error
      }
      if ('code' in data && typeof data.code === 'string') {
        code = data.code as ApiErrorCode
      }
    }
    return new ApiError(response.status, message, code, data)
  }
}
