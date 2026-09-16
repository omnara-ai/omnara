import { Readable } from 'node:stream'

import { type OperationAttemptContext, parseRetryAfter } from '../operations/retry'
import { ProviderDeliveryError } from '../types'
import { GatewayAtCapacityError } from '../work-budget'
import {
  slackEnvelope,
  type SlackMethod,
  type SlackRequest,
  type SlackResponse,
  slackResponses,
} from './protocol'

const responseBytes = 1024 * 1024 // Matches the native Slack control-plane client.
const transientCodes = new Set([
  'internal_error',
  'fatal_error',
  'service_unavailable',
  'request_timeout',
])
const publicCodes = new Set([
  'invalid_auth',
  'not_authed',
  'token_revoked',
  'account_inactive',
  'missing_scope',
  'not_allowed_token_type',
  'not_in_channel',
  'channel_not_found',
  'thread_not_found',
  'no_permission',
  'restricted_action',
  'is_archived',
  'msg_too_long',
  'file_not_found',
  'file_type_not_allowed',
  'invalid_arguments',
  'ratelimited',
  'already_reacted',
  'message_not_found',
])

export class SlackAPIError extends ProviderDeliveryError {
  constructor(
    readonly code: string,
    options: { retryable?: boolean; outcomeUnknown?: boolean; retryAfterMs?: number } = {},
  ) {
    super(`Slack operation failed: ${code}`, options)
    this.name = 'SlackAPIError'
  }
}

export interface SlackUpload {
  filename: string
  sizeBytes: number
  /** Reopens an already-authorized artifact for a safe, pre-publication retry. */
  open(): Readable
}

/** One installation's transport. No automatic retries, redirects, or shared cancellation.
 * apiUrl is deployment/test configuration, never a tool argument or provider response.
 */
export class SlackClient {
  private readonly base: URL

  constructor(
    private readonly botToken: string,
    apiUrl = 'https://slack.com/api/',
  ) {
    this.base = new URL(apiUrl.endsWith('/') ? apiUrl : `${apiUrl}/`)
    if (
      !botToken ||
      ['\r', '\n', '\u0000'].some((character) => botToken.includes(character)) ||
      this.base.username ||
      this.base.password ||
      this.base.search ||
      this.base.hash ||
      (this.base.protocol !== 'https:' && !isLoopbackURL(this.base))
    )
      throw new SlackAPIError('invalid_configuration')
  }

  async api<M extends SlackMethod>(
    method: M,
    payload: SlackRequest[M],
    context: OperationAttemptContext,
  ): Promise<SlackResponse<M>> {
    const publication = method === 'chat.postMessage' || method === 'files.completeUploadExternal'
    const body = JSON.stringify(payload)
    if (Buffer.byteLength(body) > 512 * 1024) throw new SlackAPIError('request_too_large')
    const { bytes } = await this.request(
      new URL(method, this.base),
      {
        method: 'POST',
        headers: {
          authorization: `Bearer ${this.botToken}`,
          'content-type': 'application/json; charset=utf-8',
        },
        body,
      },
      context,
      publication,
    )
    let result: unknown
    try {
      result = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes))
    } catch {
      throw new SlackAPIError('invalid_response', {
        outcomeUnknown: publication,
        retryable: !publication,
      })
    }
    const envelope = slackEnvelope.safeParse(result)
    if (!envelope.success) {
      throw new SlackAPIError('invalid_response', {
        outcomeUnknown: publication,
        retryable: !publication,
      })
    }
    if (!envelope.data.ok) {
      const code = envelope.data.error ?? ''
      if (code === 'ratelimited') {
        // Without valid guidance, stop rather than inventing a short rate-limit delay.
        throw new SlackAPIError(code, { retryable: true, retryAfterMs: Infinity })
      }
      if (transientCodes.has(code)) {
        throw new SlackAPIError('provider_unavailable', {
          outcomeUnknown: publication,
          retryable: !publication,
        })
      }
      throw new SlackAPIError(publicCodes.has(code) ? code : 'provider_rejected', {
        outcomeUnknown: publication && !publicCodes.has(code),
      })
    }
    const parsed = slackResponses[method].safeParse(result)
    if (!parsed.success)
      throw new SlackAPIError('invalid_response', { outcomeUnknown: publication })
    // SAFETY: The response was parsed with precisely the schema indexed by method.
    return parsed.data as SlackResponse<M>
  }

  async upload(url: string, file: SlackUpload, context: OperationAttemptContext): Promise<void> {
    const destination = this.fileURL(url, 'invalid_upload_url')
    if (!Number.isSafeInteger(file.sizeBytes) || file.sizeBytes < 0)
      throw new SlackAPIError('invalid_artifact')
    context.signal.throwIfAborted()
    const source = file.open()
    const state = { invalidLength: false }
    async function* chunks(): AsyncGenerator<Buffer> {
      let bytes = 0
      for await (const chunk of source) {
        const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
        bytes += buffer.byteLength
        if (bytes > file.sizeBytes) {
          state.invalidLength = true
          throw new SlackAPIError('artifact_size_mismatch')
        }
        yield buffer
      }
      if (bytes !== file.sizeBytes) {
        state.invalidLength = true
        throw new SlackAPIError('artifact_size_mismatch')
      }
    }
    const body = Readable.from(chunks())
    const stop = (): void => {
      source.destroy()
      body.destroy()
    }
    context.signal.addEventListener('abort', stop, { once: true })
    try {
      // SAFETY: chunks() emits only Buffer (Uint8Array), and toWeb preserves those chunks.
      const stream = Readable.toWeb(body) as ReadableStream<Uint8Array>
      const init: RequestInit & { duplex: 'half' } = {
        method: 'POST',
        body: stream,
        duplex: 'half',
        headers: {
          'content-type': 'application/octet-stream',
          'content-length': String(file.sizeBytes),
        },
      }
      // Upload tickets contain their own authorization; never send the bot token here.
      await this.request(destination, init, context, false)
    } catch (error) {
      if (state.invalidLength) throw new SlackAPIError('artifact_size_mismatch')
      throw error
    } finally {
      context.signal.removeEventListener('abort', stop)
      stop()
    }
  }

  /** Authenticated private-file download. The limit covers streamed bytes even
   * when Slack omits or understates Content-Length; redirects never receive auth.
   */
  download(
    url: string,
    maxBytes: number,
    context: OperationAttemptContext,
    reserveBytes?: (bytes: number) => void,
  ) {
    if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0) throw new SlackAPIError('file_too_large')
    return this.request(
      this.fileURL(url, 'invalid_file_url'),
      { method: 'GET', headers: { authorization: `Bearer ${this.botToken}` } },
      context,
      false,
      maxBytes,
      'file_too_large',
      reserveBytes,
    )
  }

  private fileURL(raw: string, invalidCode: string): URL {
    let url: URL
    try {
      url = new URL(raw)
    } catch {
      throw new SlackAPIError(invalidCode)
    }
    const slackHost = ['slack.com', 'slack-edge.com', 'slack-files.com'].some(
      (host) => url.hostname === host || url.hostname.endsWith(`.${host}`),
    )
    const testOrigin = isLoopbackURL(this.base) && url.origin === this.base.origin
    if (
      url.username ||
      url.password ||
      url.hash ||
      (!testOrigin && (url.protocol !== 'https:' || url.port !== '' || !slackHost))
    ) {
      throw new SlackAPIError(invalidCode)
    }
    return url
  }

  private async request(
    url: URL,
    init: RequestInit,
    context: OperationAttemptContext,
    publication: boolean,
    maxBytes = responseBytes,
    oversizedCode = 'response_too_large',
    reserveBytes?: (bytes: number) => void,
  ): Promise<{ bytes: Buffer; contentType: string }> {
    const remaining = context.deadlineMs - Date.now()
    if (!Number.isSafeInteger(remaining) || remaining <= 0 || remaining > 2_147_483_647) {
      throw new SlackAPIError('deadline_exceeded')
    }
    context.signal.throwIfAborted()
    const controller = new AbortController()
    const timer = setTimeout(() => {
      controller.abort()
    }, remaining)
    const signal = AbortSignal.any([context.signal, controller.signal])
    try {
      const response = await fetch(url, { ...init, redirect: 'manual', signal })
      if (!response.ok) {
        await response.body?.cancel()
        if (response.status === 429) {
          throw new SlackAPIError('ratelimited', {
            retryable: true,
            retryAfterMs: parseRetryAfter(response.headers.get('retry-after')) ?? Infinity,
          })
        }
        const transient = response.status >= 500 || response.status === 408
        throw new SlackAPIError(transient ? 'provider_unavailable' : 'http_rejected', {
          retryable: transient && !publication,
          outcomeUnknown: transient && publication,
        })
      }
      if (!response.body)
        throw new SlackAPIError('invalid_response', { outcomeUnknown: publication })
      const declared = response.headers.get('content-length')
      if (declared && /^\d+$/.test(declared) && BigInt(declared) > BigInt(maxBytes)) {
        await response.body.cancel()
        throw new SlackAPIError(oversizedCode, { outcomeUnknown: publication })
      }
      const reader = response.body.getReader()
      const chunks: Uint8Array[] = []
      let size = 0
      try {
        for (;;) {
          const { done, value } = await reader.read()
          if (done) break
          size += value.byteLength
          if (size > maxBytes)
            throw new SlackAPIError(oversizedCode, { outcomeUnknown: publication })
          reserveBytes?.(size)
          chunks.push(value)
        }
      } finally {
        await reader.cancel().catch(() => undefined)
        reader.releaseLock()
      }
      return {
        bytes: Buffer.concat(chunks, size),
        contentType: response.headers.get('content-type') ?? '',
      }
    } catch (error) {
      if (error instanceof SlackAPIError || error instanceof GatewayAtCapacityError) throw error
      throw new SlackAPIError('transport_failed', {
        outcomeUnknown: publication,
        retryable: !publication,
      })
    } finally {
      clearTimeout(timer)
      controller.abort()
    }
  }
}

function isLoopbackURL(url: URL): boolean {
  return url.protocol === 'http:' && ['127.0.0.1', '[::1]', 'localhost'].includes(url.hostname)
}
