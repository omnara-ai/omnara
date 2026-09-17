import { randomBytes } from 'node:crypto'
import { Readable } from 'node:stream'

import type { JsonBody } from '@omnara/sdk'
import { z } from 'zod'

import { parseObjectFields } from '../json'
import type { OperationArtifact } from '../operations/files'
import { type OperationAttemptContext, parseRetryAfter } from '../operations/retry'
import type { DiscordConfiguration } from './configuration'
import {
  DiscordAPIError,
  discordChannel,
  discordID,
  discordMessage,
  discordUser,
  providerValue,
} from './protocol'

const responseBytes = 1024 * 1024
export const discordRequestBytes = 25 * 1024 * 1024
export type DiscordUpload = Pick<
  OperationArtifact,
  'id' | 'filename' | 'content_type' | 'sizeBytes' | 'open'
>
const application = z.object({ id: discordID })
const rateLimit = z.object({ retry_after: z.number().nonnegative() })

/** Fixed Discord REST endpoints. apiUrl is deployment/test configuration only.
 * No hidden retries, redirect following, shared abort state, or file buffering.
 */
export class DiscordClient {
  readonly configuration: Readonly<DiscordConfiguration>
  private readonly base: URL
  private verified = false

  constructor(configuration: DiscordConfiguration, apiUrl = 'https://discord.com/api/v10/') {
    this.configuration = Object.freeze({ ...configuration })
    try {
      this.base = new URL(apiUrl.endsWith('/') ? apiUrl : `${apiUrl}/`)
      const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(this.base.hostname)
      if (
        this.base.username ||
        this.base.password ||
        this.base.search ||
        this.base.hash ||
        (this.base.protocol !== 'https:' && !(loopback && this.base.protocol === 'http:')) ||
        !/^[\x21-\x7e]{1,4096}$/.test(configuration.botToken) ||
        ![configuration.applicationID, configuration.botUserID, configuration.guildID].every(
          (id) => discordID.safeParse(id).success,
        )
      )
        throw new Error('invalid configuration')
    } catch {
      throw new DiscordAPIError('invalid_configuration')
    }
  }

  async verifyIdentity(context: OperationAttemptContext): Promise<void> {
    if (this.verified) return
    const app = providerValue(application, await this.request('GET', 'applications/@me', context))
    const user = providerValue(discordUser, await this.request('GET', 'users/@me', context))
    if (
      app.id !== this.configuration.applicationID ||
      user.id !== this.configuration.botUserID ||
      user.bot !== true
    )
      throw new DiscordAPIError('identity_mismatch')
    this.verified = true
  }

  async getChannel(id: string, context: OperationAttemptContext) {
    validateID(id)
    return providerValue(discordChannel, await this.request('GET', `channels/${id}`, context))
  }

  async getMessages(
    id: string,
    limit: number,
    before: string | undefined,
    context: OperationAttemptContext,
  ) {
    validateID(id)
    if (!Number.isInteger(limit) || limit < 1 || limit > 100)
      throw new DiscordAPIError('invalid_request')
    if (before !== undefined) validateID(before)
    const query = new URLSearchParams({ limit: String(limit) })
    if (before !== undefined) query.set('before', before)
    return providerValue(
      z.array(discordMessage).max(limit),
      await this.request('GET', `channels/${id}/messages?${query}`, context),
    )
  }

  async createMessage(
    id: string,
    content: string | undefined,
    files: readonly DiscordUpload[],
    context: OperationAttemptContext,
  ) {
    validateID(id)
    this.validateMessage(content, files)
    const payload = messagePayload(content, files)
    const result = files.length
      ? await this.multipart(`channels/${id}/messages`, payload, files, context)
      : await this.request('POST', `channels/${id}/messages`, context, JSON.stringify(payload))
    return providerValue(discordMessage, result, true)
  }

  async createThread(
    channel: string,
    message: string,
    name: string,
    context: OperationAttemptContext,
  ) {
    validateID(channel)
    validateID(message)
    if (!name.trim() || name.length > 100 || Buffer.from(name).toString('utf8') !== name)
      throw new DiscordAPIError('invalid_thread_name')
    return providerValue(
      discordChannel,
      await this.request(
        'POST',
        `channels/${channel}/messages/${message}/threads`,
        context,
        JSON.stringify({ name }),
      ),
      true,
    )
  }

  /** Checks declared total before any provider I/O; streaming checks every file's
   * actual bytes as well. Omnara artifacts are already staged on bounded disk.
   */
  validateMessage(content: string | undefined, files: readonly DiscordUpload[]): void {
    if (
      (content !== undefined &&
        (content.length > 2000 || Buffer.from(content).toString('utf8') !== content)) ||
      (!content?.trim() && !files.length)
    )
      throw new DiscordAPIError('invalid_message')
    if (files.length > 10) throw new DiscordAPIError('too_many_artifacts')
    this.validateFiles(0, files)
    // The random boundary always has the same byte length.
    const parts = multipartParts(messagePayload(content, files), files)
    this.validateFiles(parts.overhead, files)
  }

  private validateFiles(payloadBytes: number, files: readonly DiscordUpload[]): void {
    let total = payloadBytes
    for (const file of files) {
      if (
        !Number.isSafeInteger(file.sizeBytes) ||
        file.sizeBytes < 0 ||
        !file.filename ||
        Buffer.byteLength(file.filename) > 512 ||
        Buffer.from(file.filename).toString('utf8') !== file.filename ||
        ['\r', '\n', '\u0000'].some((character) => file.filename.includes(character)) ||
        !/^[\x21-\x7e]+$/.test(file.content_type)
      )
        throw new DiscordAPIError('invalid_artifact')
      total += file.sizeBytes
    }
    if (!Number.isSafeInteger(total) || total > discordRequestBytes)
      throw new DiscordAPIError('request_too_large')
  }

  private async multipart(
    path: string,
    payload: JsonBody,
    files: readonly DiscordUpload[],
    context: OperationAttemptContext,
  ): Promise<JsonBody> {
    const signal = AbortSignal.any([
      context.signal,
      AbortSignal.timeout(Math.min(2_147_483_647, Math.max(1, context.deadlineMs - Date.now()))),
    ])
    const { boundary, headers, first, last, overhead } = multipartParts(payload, files)
    this.validateFiles(overhead, files)
    const length = overhead + files.reduce((n, file) => n + file.sizeBytes, 0)
    async function* chunks(): AsyncGenerator<Buffer> {
      yield first
      for (const [index, file] of files.entries()) {
        yield headers[index] ?? Buffer.alloc(0)
        const stream = file.open()
        const stop = () => {
          stream.destroy()
        }
        signal.addEventListener('abort', stop, { once: true })
        let bytes = 0
        try {
          signal.throwIfAborted()
          for await (const chunk of stream) {
            const value = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
            bytes += value.length
            if (bytes > file.sizeBytes) throw new DiscordAPIError('artifact_size_mismatch')
            yield value
          }
          if (bytes !== file.sizeBytes) throw new DiscordAPIError('artifact_size_mismatch')
        } finally {
          signal.removeEventListener('abort', stop)
          stream.destroy()
        }
        yield Buffer.from('\r\n')
      }
      yield last
    }
    const body = Readable.from(chunks())
    try {
      // SAFETY: chunks emits only Buffer; toWeb preserves the Uint8Array chunks.
      return await this.request(
        'POST',
        path,
        { ...context, signal },
        Readable.toWeb(body) as ReadableStream<Uint8Array>,
        {
          'content-type': `multipart/form-data; boundary=${boundary}`,
          'content-length': String(length),
        },
      )
    } finally {
      body.destroy()
    }
  }

  private request(
    method: 'GET' | 'POST',
    path: string,
    context: OperationAttemptContext,
    body?: string | ReadableStream<Uint8Array>,
    headers?: Record<string, string>,
  ): Promise<JsonBody> {
    return discordRequest(
      this.base,
      this.configuration.botToken,
      method,
      path,
      context,
      body,
      headers,
    )
  }
}

/** Shared bounded transport for native app bootstrap and installed resources. */
export async function discordRequest(
  base: URL,
  botToken: string,
  method: 'GET' | 'POST',
  path: string,
  context: OperationAttemptContext,
  body?: string | ReadableStream<Uint8Array>,
  headers?: Record<string, string>,
): Promise<JsonBody> {
  context.signal.throwIfAborted()
  if (Date.now() >= context.deadlineMs) throw new DiscordAPIError('deadline_exceeded')
  const mutation = method === 'POST'
  const signal = AbortSignal.any([
    context.signal,
    AbortSignal.timeout(Math.min(2_147_483_647, Math.max(1, context.deadlineMs - Date.now()))),
  ])
  let response: Response | undefined
  try {
    const init: RequestInit & { duplex?: 'half' } = {
      method,
      body,
      signal,
      redirect: 'manual',
      headers: {
        authorization: `Bot ${botToken}`,
        'user-agent': 'DiscordBot (https://omnara.com, 0.0.0)',
        'content-type': 'application/json',
        ...headers,
      },
    }
    if (body instanceof ReadableStream) init.duplex = 'half'
    response = await fetch(new URL(path, base), init)
    if (!response.ok && response.status !== 429) {
      const ambiguous = response.status >= 500 || response.status === 408
      throw new DiscordAPIError(
        response.status === 404 && path.startsWith('channels/')
          ? 'address_unavailable'
          : 'http_rejected',
        {
          outcomeUnknown: mutation && (ambiguous || response.status < 400),
          retryable: !mutation && ambiguous,
        },
      )
    }
    const raw = await readBody(response)
    // Wrapping also rejects duplicate keys in history's root array.
    parseObjectFields(`{"value":${raw}}`, responseBytes + 10)
    const value = z.json().parse(JSON.parse(raw))
    if (response.status === 429) {
      const parsed = rateLimit.safeParse(value)
      const bodyDelay = parsed.success ? parsed.data.retry_after * 1000 : undefined
      const headerDelay = parseRetryAfter(response.headers.get('retry-after'))
      const delay =
        bodyDelay === undefined && headerDelay === undefined
          ? Infinity
          : Math.max(bodyDelay ?? 0, headerDelay ?? 0)
      throw new DiscordAPIError('rate_limited', { retryable: true, retryAfterMs: delay })
    }
    return value
  } catch (error) {
    if (error instanceof DiscordAPIError) throw error
    if (response?.status === 429)
      throw new DiscordAPIError('rate_limited', {
        retryable: true,
        retryAfterMs: parseRetryAfter(response.headers.get('retry-after')) ?? Infinity,
      })
    throw new DiscordAPIError('provider_unavailable', {
      outcomeUnknown: mutation,
      retryable: !mutation,
    })
  } finally {
    if (response && !response.bodyUsed) await response.body?.cancel().catch(() => undefined)
  }
}

function validateID(id: string): void {
  if (!discordID.safeParse(id).success) throw new DiscordAPIError('invalid_destination')
}

async function readBody(response: Response): Promise<string> {
  const reader = response.body?.getReader()
  if (!reader) throw new Error('missing response body')
  const chunks: Uint8Array[] = []
  let bytes = 0
  try {
    for (;;) {
      const chunk = await reader.read()
      if (chunk.done) break
      bytes += chunk.value.length
      if (bytes > responseBytes) throw new Error('response too large')
      chunks.push(chunk.value)
    }
  } finally {
    await reader.cancel().catch(() => undefined)
  }
  return new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(chunks, bytes))
}

function messagePayload(content: string | undefined, files: readonly DiscordUpload[]) {
  const payload = {
    allowed_mentions: { parse: [] },
    attachments: files.map((file, index) => ({ id: index, filename: file.filename })),
  }
  return content === undefined ? payload : { ...payload, content }
}

function multipartParts(payload: JsonBody, files: readonly DiscordUpload[]) {
  const boundary = `omnara-${randomBytes(18).toString('hex')}`
  const headers = files.map((file, index) => {
    const filename = file.filename.replace(/["\\]/g, (character) => `\\${character}`)
    return Buffer.from(
      `--${boundary}\r\nContent-Disposition: form-data; name="files[${index}]"; filename="${filename}"\r\nContent-Type: ${file.content_type}\r\n\r\n`,
    )
  })
  const first = Buffer.from(
    `--${boundary}\r\nContent-Disposition: form-data; name="payload_json"\r\nContent-Type: application/json\r\n\r\n${JSON.stringify(payload)}\r\n`,
  )
  const last = Buffer.from(`--${boundary}--\r\n`)
  const overhead = first.length + last.length + headers.reduce((n, h) => n + h.length + 2, 0)
  return { boundary, headers, first, last, overhead }
}
