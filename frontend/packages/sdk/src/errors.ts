import * as z from 'zod'

import type { AgentConfigErrorIssue, Error as ApiErrorBody } from './generated/types.gen'
import { zAgentConfigErrorIssue, zError } from './generated/zod.gen'
import { type JsonBody, jsonBody } from './json-body'

export type ApiErrorCode = ApiErrorBody['code']

const errorIssuesSchema = z.object({ issues: z.array(zAgentConfigErrorIssue).optional() })

const errorResponseSchema = zError
  .pick({ code: true })
  .partial()
  .extend({ error: z.string().optional(), error_description: z.string().optional() })

function issuesFromBody(body: JsonBody | undefined): AgentConfigErrorIssue[] {
  const parsed = errorIssuesSchema.safeParse(body)
  return parsed.success ? (parsed.data.issues ?? []) : []
}

export class ApiError extends Error {
  readonly status: number
  readonly code: ApiErrorCode | undefined
  readonly body: JsonBody | undefined
  readonly issues: AgentConfigErrorIssue[]

  constructor(status: number, message: string, code?: ApiErrorCode, body?: JsonBody) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.body = body
    this.issues = issuesFromBody(body)
  }

  static async fromResponse(response: Response): Promise<ApiError> {
    return ApiError.fromBody(response.status, await jsonBody(response))
  }

  static fromBody(status: number, body: JsonBody | undefined): ApiError {
    const parsed = errorResponseSchema.safeParse(body)
    const message =
      parsed.data?.error_description ?? parsed.data?.error ?? `request failed with status ${status}`
    return new ApiError(status, message, parsed.data?.code, body)
  }
}
