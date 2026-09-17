import { Readable } from 'node:stream'

import { ProviderResponseTooLargeError, readProviderResponseBody } from '../http-io'
import { type OperationAttemptContext, parseRetryAfter } from '../operations/retry'
import { GatewayAtCapacityError } from '../work-budget'
import { SlackMessagingAdapter } from './adapter'
import { SlackAPIError } from './errors'
import type { SlackMethod, SlackRequest, SlackResponse } from './protocol'

const responseBytes = 1024 * 1024 // Matches the native Slack control-plane client.
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
  private readonly adapter: SlackMessagingAdapter

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
    this.adapter = new SlackMessagingAdapter(botToken, this.base.href)
  }

  async api<M extends SlackMethod>(
    method: M,
    payload: SlackRequest[M],
    context: OperationAttemptContext,
  ): Promise<SlackResponse<M>> {
    return this.adapter.api(method, payload, context)
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
      await this.requestFile(destination, init, context)
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
    return this.requestFile(
      this.fileURL(url, 'invalid_file_url'),
      { method: 'GET', headers: { authorization: `Bearer ${this.botToken}` } },
      context,
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

  private async requestFile(
    url: URL,
    init: RequestInit,
    context: OperationAttemptContext,
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
          retryable: transient,
        })
      }
      if (!response.body) throw new SlackAPIError('invalid_response')
      return {
        bytes: await readProviderResponseBody(response, maxBytes, signal, reserveBytes),
        contentType: response.headers.get('content-type') ?? '',
      }
    } catch (error) {
      if (error instanceof SlackAPIError || error instanceof GatewayAtCapacityError) throw error
      if (error instanceof ProviderResponseTooLargeError) throw new SlackAPIError(oversizedCode)
      throw new SlackAPIError('transport_failed', { retryable: true })
    } finally {
      clearTimeout(timer)
      controller.abort()
    }
  }
}

function isLoopbackURL(url: URL): boolean {
  return url.protocol === 'http:' && ['127.0.0.1', '[::1]', 'localhost'].includes(url.hostname)
}
