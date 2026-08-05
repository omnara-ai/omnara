import type { Error as ApiErrorBody } from './generated/types.gen'

export type ApiErrorCode = ApiErrorBody['code']

export class ApiError extends Error {
  readonly status: number
  readonly code: ApiErrorCode | undefined

  constructor(status: number, message: string, code?: ApiErrorCode) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
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
    return new ApiError(response.status, message, code)
  }
}
