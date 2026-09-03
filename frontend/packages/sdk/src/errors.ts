import * as z from 'zod'

import type { AgentConfigErrorIssue, Error as ApiErrorBody } from './generated/types.gen'
import { zError } from './generated/zod.gen'
import { type JsonBody, zJsonBody, zJsonText } from './json-body'
import { relaxedSchema } from './validate-response'

export type ApiErrorCode = ApiErrorBody['code']

const zErrorBody = zJsonText.pipe(zJsonBody)

export const zErrorResponse = relaxedSchema(
  zError.partial({ code: true }).extend({ error_description: z.string().optional() }),
)

function issuesFromBody(body: JsonBody | undefined): AgentConfigErrorIssue[] {
  const parsed = zErrorResponse.safeParse(body)
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
    const body = zErrorBody.safeParse(await response.clone().text())
    return ApiError.fromBody(response.status, body.success ? body.data : undefined)
  }

  static fromBody(status: number, body: JsonBody | undefined): ApiError {
    const parsed = zErrorResponse.safeParse(body)
    const message =
      parsed.data?.error_description ?? parsed.data?.error ?? `request failed with status ${status}`
    return new ApiError(status, message, parsed.data?.code, body)
  }
}
