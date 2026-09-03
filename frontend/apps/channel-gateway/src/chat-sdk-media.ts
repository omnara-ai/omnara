import type { CreateAgentInputContentBlock } from '@omnara/sdk'
import type { Attachment, Message } from 'chat'

import type { ProviderWebhookContext, ProviderWorkReservation } from './types'

const supportedMediaTypes = new Set([
  'application/pdf',
  'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  'image/gif',
  'image/jpeg',
  'image/png',
  'image/webp',
  'text/csv',
  'text/markdown',
  'text/plain',
  'text/tab-separated-values',
] as const)

const channelContentBlockLimit = 100
const channelMediaFilenameLimit = 255
const channelMediaItemLimit = 20
const defaultMediaFetchTimeoutMs = 20_000
const maxInboundChannelTextBytes = 1024 * 1024
const mediaDownloadReservationMultiplier = 2
const mediaRetainedReservationMultiplier = 4
const textRetainedReservationMultiplier = 3

type SupportedMediaType = typeof supportedMediaTypes extends Set<infer T> ? T : never

export interface ChatSdkAttachmentLoadContext {
  maxBytes: number
  signal: AbortSignal
}

// Provider factories own provider authentication and URL interpretation. This
// loader must enforce maxBytes while streaming and pass signal to the network
// request; fetchBoundedMedia is provided for the common HTTP case.
export type ChatSdkAttachmentDataLoader = (
  attachment: Attachment,
  context: ChatSdkAttachmentLoadContext,
) => Promise<ArrayBuffer | Buffer>

export async function messageContentBlocks(
  message: Message,
  maxMediaItemBytes: number,
  maxMediaTotalBytes: number,
  options: {
    fetchAttachmentData?: ChatSdkAttachmentDataLoader
    fetchTimeoutMs?: number
    reserveWorkBytes?: ProviderWebhookContext['reserveWorkBytes']
    signal?: AbortSignal
  } = {},
): Promise<CreateAgentInputContentBlock[]> {
  const blocks: CreateAgentInputContentBlock[] = []
  const hasText = message.text.trim() !== ''
  const textBytes = Buffer.byteLength(message.text, 'utf8')
  if (textBytes > maxInboundChannelTextBytes) {
    throw new Error('inbound channel message text exceeds its byte limit')
  }
  if (hasText) {
    options.reserveWorkBytes?.(textBytes * textRetainedReservationMultiplier)
    blocks.push({ text: message.text, type: 'text' })
  }
  const supportedCount = message.attachments.filter(isSupportedMediaAttachment).length
  const candidateCount =
    message.attachments.length - supportedCount + Math.min(supportedCount, channelMediaItemLimit)
  const needsOmissionNotice =
    supportedCount > channelMediaItemLimit ||
    blocks.length + candidateCount > channelContentBlockLimit
  const attachmentBlockLimit =
    channelContentBlockLimit - blocks.length - (needsOmissionNotice ? 1 : 0)
  const retainedAttachments: Attachment[] = []
  let mediaSeen = 0
  let omittedAttachments = 0
  for (const attachment of message.attachments) {
    if (isSupportedMediaAttachment(attachment)) {
      mediaSeen += 1
      if (mediaSeen > channelMediaItemLimit) {
        omittedAttachments += 1
        continue
      }
    }
    if (retainedAttachments.length >= attachmentBlockLimit) {
      omittedAttachments += 1
      continue
    }
    retainedAttachments.push(attachment)
  }
  let mediaBytes = 0
  for (const attachment of retainedAttachments) {
    const mimeType = attachment.mimeType
    if (!mimeType || !supportedMediaTypes.has(mimeType as SupportedMediaType)) {
      blocks.push(attachmentNotice(attachment, 'unsupported media type'))
      continue
    }
    const knownSize =
      attachment.size ??
      (Buffer.isBuffer(attachment.data)
        ? attachment.data.byteLength
        : attachment.data instanceof Blob
          ? attachment.data.size
          : undefined)
    if (attachment.fetchData && knownSize === undefined) {
      throw new Error('inbound channel attachment must declare its size before download')
    }
    if (knownSize !== undefined && knownSize > maxMediaItemBytes) {
      throw new Error('inbound channel attachment exceeds the per-item byte limit')
    }
    if (knownSize !== undefined && mediaBytes + knownSize > maxMediaTotalBytes) {
      throw new Error('inbound channel media exceeds the configured byte limit')
    }
    const remainingBytes = Math.min(maxMediaItemBytes, maxMediaTotalBytes - mediaBytes)
    let workReservation: ProviderWorkReservation | undefined
    if (options.reserveWorkBytes) {
      const initialBytes = attachment.fetchData
        ? remainingBytes * mediaDownloadReservationMultiplier
        : (knownSize ?? remainingBytes) * mediaRetainedReservationMultiplier
      workReservation = options.reserveWorkBytes(initialBytes)
    }
    let data: Buffer | undefined
    try {
      data = await attachmentBytes(attachment, remainingBytes, options)
      workReservation?.resize((data?.byteLength ?? 0) * mediaRetainedReservationMultiplier)
    } catch (error) {
      workReservation?.release()
      throw error
    }
    if (!data) {
      blocks.push(attachmentNotice(attachment, 'attachment data was unavailable'))
      continue
    }
    if (data.byteLength > maxMediaItemBytes) {
      throw new Error('inbound channel attachment exceeds the per-item byte limit')
    }
    mediaBytes += data.byteLength
    if (mediaBytes > maxMediaTotalBytes) {
      throw new Error('inbound channel media exceeds the configured byte limit')
    }
    blocks.push({
      data: data.toString('base64'),
      filename: normalizedAttachmentFilename(attachment.name),
      media_type: mimeType as SupportedMediaType,
      type: 'media',
    })
  }
  if (omittedAttachments > 0) {
    blocks.push({
      text: `[${omittedAttachments} additional channel attachments omitted]`,
      type: 'text',
    })
  }
  if (blocks.length === 0) blocks.push({ text: '[Empty channel message]', type: 'text' })
  return blocks
}

async function attachmentBytes(
  attachment: Attachment,
  maxBytes: number,
  options: {
    fetchAttachmentData?: ChatSdkAttachmentDataLoader
    fetchTimeoutMs?: number
    signal?: AbortSignal
  },
): Promise<Buffer | undefined> {
  if (attachment.fetchData) {
    if (!options.fetchAttachmentData) {
      throw new Error('remote channel attachment requires a bounded attachment loader')
    }
    const timeoutSignal = AbortSignal.timeout(options.fetchTimeoutMs ?? defaultMediaFetchTimeoutMs)
    const signal = options.signal ? AbortSignal.any([options.signal, timeoutSignal]) : timeoutSignal
    const data = await raceWithAbortReason(
      options.fetchAttachmentData(attachment, { maxBytes, signal }),
      signal,
    )
    return Buffer.isBuffer(data) ? data : Buffer.from(data)
  }
  if (attachment.data instanceof Blob) return Buffer.from(await attachment.data.arrayBuffer())
  if (Buffer.isBuffer(attachment.data)) return attachment.data
  return undefined
}

export async function fetchBoundedMedia(
  input: RequestInfo | URL,
  init: RequestInit,
  context: ChatSdkAttachmentLoadContext,
): Promise<Buffer> {
  const response = await fetch(input, { ...init, signal: context.signal })
  if (!response.ok) throw new Error(`provider media request failed with status ${response.status}`)
  const declared = response.headers.get('content-length')?.trim()
  if (declared && /^\d+$/.test(declared) && BigInt(declared) > BigInt(context.maxBytes)) {
    await response.body?.cancel()
    throw new Error('inbound channel attachment exceeds the bounded media byte limit')
  }
  if (!response.body) return Buffer.alloc(0)

  const reader = response.body.getReader()
  const chunks: Buffer[] = []
  let size = 0
  let completed = false
  try {
    while (true) {
      const result = await raceWithAbortReason(reader.read(), context.signal)
      if (result.done) {
        completed = true
        return Buffer.concat(chunks, size)
      }
      size += result.value.byteLength
      if (size > context.maxBytes) {
        throw new Error('inbound channel attachment exceeds the bounded media byte limit')
      }
      chunks.push(Buffer.from(result.value))
    }
  } finally {
    if (completed) {
      reader.releaseLock()
    } else {
      void reader
        .cancel(context.signal.reason)
        .then(() => {
          reader.releaseLock()
        })
        .catch(() => undefined)
    }
  }
}

function raceWithAbortReason<T>(work: Promise<T>, signal: AbortSignal): Promise<T> {
  if (signal.aborted) return Promise.reject(abortError(signal))
  return new Promise<T>((resolve, reject) => {
    const onAbort = (): void => {
      reject(abortError(signal))
    }
    signal.addEventListener('abort', onAbort, { once: true })
    void work.then(
      (value) => {
        signal.removeEventListener('abort', onAbort)
        resolve(value)
      },
      (error: unknown) => {
        signal.removeEventListener('abort', onAbort)
        reject(error instanceof Error ? error : new Error(String(error)))
      },
    )
  })
}

function abortError(signal: AbortSignal): Error {
  return signal.reason instanceof Error ? signal.reason : new Error('channel media read aborted')
}

function normalizedAttachmentFilename(name: string | undefined): string | undefined {
  if (name === undefined) return undefined
  if (Buffer.byteLength(name, 'utf8') <= channelMediaFilenameLimit) return name
  const characters: string[] = []
  let bytes = 0
  for (const character of name) {
    const characterBytes = Buffer.byteLength(character, 'utf8')
    if (bytes + characterBytes > channelMediaFilenameLimit) break
    characters.push(character)
    bytes += characterBytes
  }
  return characters.join('')
}

function isSupportedMediaAttachment(attachment: Attachment): boolean {
  return (
    attachment.mimeType !== undefined &&
    supportedMediaTypes.has(attachment.mimeType as SupportedMediaType)
  )
}

function attachmentNotice(attachment: Attachment, reason: string): CreateAgentInputContentBlock {
  const candidate = normalizedAttachmentFilename(attachment.name)?.trim()
  const name = candidate === undefined || candidate === '' ? 'unnamed attachment' : candidate
  return { text: `[${name}: ${reason}]`, type: 'text' }
}
