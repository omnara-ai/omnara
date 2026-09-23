import * as z from 'zod'

import type { AgentConfigErrorIssue, ErrorCode } from './generated/types.gen'
import { zError } from './generated/zod.gen'
import { type JsonBody, zJsonBody, zJsonText } from './json-body'
import { relaxedSchema } from './validate-response'

export type ApiErrorCode = ErrorCode | (string & {})

const zErrorBody = zJsonText.pipe(zJsonBody)

export const zErrorResponse = relaxedSchema(
  zError.partial({ code: true }).extend({ error_description: z.string().optional() }),
)

export class ApiError extends Error {
  readonly status: number
  readonly code: ApiErrorCode | undefined
  readonly body: JsonBody | undefined
  readonly issues: AgentConfigErrorIssue[]
  readonly currentDigest: string | undefined

  constructor(status: number, message: string, code?: ApiErrorCode, body?: JsonBody) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.body = body
    const parsed = zErrorResponse.safeParse(body)
    this.issues = parsed.data?.issues ?? []
    this.currentDigest = parsed.data?.current_digest
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
