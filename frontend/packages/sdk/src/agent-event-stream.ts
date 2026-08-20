import type { OmnaraClient } from './client'
import { streamEvents } from './generated/sdk.gen'
import type { AgentEventStreamData, StreamEventsData } from './generated/types.gen'
import { zStreamEventsResponse } from './generated/zod.gen'
import { isUnknownEnumError } from './validate-response'

export type AgentEventStreamErrorKind = 'aborted' | 'contract' | 'http' | 'transport'

export class AgentEventStreamError extends Error {
  readonly kind: AgentEventStreamErrorKind
  readonly retryable: boolean
  readonly status: number | undefined

  constructor({
    kind,
    message,
    retryable,
    status,
    cause,
  }: {
    kind: AgentEventStreamErrorKind
    message: string
    retryable: boolean
    status?: number
    cause?: unknown
  }) {
    super(message, { cause })
    this.name = 'AgentEventStreamError'
    this.kind = kind
    this.retryable = retryable
    this.status = status
  }
}

export interface OpenAgentEventStreamOptions {
  client: OmnaraClient
  path: StreamEventsData['path']
  query?: StreamEventsData['query']
  headers?: StreamEventsData['headers']
  signal?: AbortSignal
}

export interface AgentEventStream {
  stream: AsyncGenerator<AgentEventStreamData>
}

const generatedHTTPError = /^SSE failed: (\d{3})(?:\s|$)/

function classifyStreamError(error: unknown, signal?: AbortSignal): AgentEventStreamError {
  if (error instanceof AgentEventStreamError) return error
  if (signal?.aborted === true || (error instanceof Error && error.name === 'AbortError')) {
    return new AgentEventStreamError({
      kind: 'aborted',
      message: 'Agent event stream canceled',
      retryable: false,
      cause: error,
    })
  }
  const match = error instanceof Error ? generatedHTTPError.exec(error.message) : null
  const status = match?.[1] == null ? undefined : Number.parseInt(match[1], 10)
  if (status != null) {
    return new AgentEventStreamError({
      kind: 'http',
      message: `Agent event stream request failed with HTTP ${status}`,
      retryable: status === 408 || status === 429 || status >= 500,
      status,
      cause: error,
    })
  }
  return new AgentEventStreamError({
    kind: 'transport',
    message: 'Agent event stream disconnected',
    retryable: true,
    cause: error,
  })
}

async function* validatedFrames(
  source: AsyncIterable<unknown>,
  sourceError: () => AgentEventStreamError | undefined,
  signal?: AbortSignal,
): AsyncGenerator<AgentEventStreamData> {
  try {
    for await (const data of source) {
      const parsed = await zStreamEventsResponse.safeParseAsync(data)
      if (parsed.success) {
        yield parsed.data
      } else if (isUnknownEnumError(parsed.error, data)) {
        yield data as AgentEventStreamData
      } else {
        throw new AgentEventStreamError({
          kind: 'contract',
          message: 'Agent event stream received data that does not match the API contract',
          retryable: false,
          cause: parsed.error,
        })
      }
    }
  } catch (error) {
    throw classifyStreamError(error, signal)
  }
  const error = sourceError()
  if (error != null) throw error
}

export async function openAgentEventStream({
  client,
  path,
  query,
  headers,
  signal,
}: OpenAgentEventStreamOptions): Promise<AgentEventStream> {
  let connectionError: AgentEventStreamError | undefined
  try {
    const result = await streamEvents({
      client,
      path,
      query,
      headers,
      signal,
      responseValidator: undefined,
      sseMaxRetryAttempts: 1,
      onSseError: (error) => {
        connectionError = classifyStreamError(error, signal)
      },
    })
    return {
      stream: validatedFrames(result.stream, () => connectionError, signal),
    }
  } catch (error) {
    throw classifyStreamError(error, signal)
  }
}
