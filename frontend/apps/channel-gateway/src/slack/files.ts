import { isUtf8 } from 'node:buffer'
import { extname } from 'node:path'

import { type InlineMediaContentBlock, schemas } from '@omnara/sdk'

import type { OperationAttemptContext } from '../operations/retry'
import type { ProviderWorkReservation } from '../types'
import { type SlackClient } from './client'
import { SlackAPIError } from './errors'
import type { SlackEventFile } from './protocol'

// Existing native Slack/input admission budgets, not provider schema policy.
const fileBytes = 10 * 1024 * 1024
const totalBytes = 24 * 1024 * 1024
const maxFiles = 20

type MediaType = InlineMediaContentBlock['media_type']
// File-name inference mirrors modelcontext/mediatypes.go. The generated schema
// remains the authority for which MIME types can enter a canonical input.
const extensions: Record<MediaType, string> = {
  'image/png': '.png',
  'image/jpeg': '.jpg',
  'image/gif': '.gif',
  'image/webp': '.webp',
  'application/pdf': '.pdf',
  'text/plain': '.txt',
  'text/markdown': '.md',
  'text/csv': '.csv',
  'text/tab-separated-values': '.tsv',
  'text/x-iif': '.iif',
  'application/msword': '.doc',
  'application/rtf': '.rtf',
  'application/vnd.oasis.opendocument.text': '.odt',
  'application/vnd.apple.pages': '.pages',
  'application/vnd.apple.keynote': '.key',
  'application/vnd.apple.iwork': '.iwork',
  'application/vnd.ms-powerpoint': '.ppt',
  'application/vnd.ms-excel': '.xls',
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document': '.docx',
  'application/vnd.openxmlformats-officedocument.presentationml.presentation': '.pptx',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet': '.xlsx',
}

interface FileResult {
  ordinal?: number
  id?: string
  name?: string
  title?: string
  mimetype?: string
  declared_size_bytes?: number
  file_access?: string
  status: 'stored' | 'skipped'
  reason?: string
  count?: number
  content_type?: MediaType
  filename?: string
  size_bytes?: number
}
export interface SlackInputFiles {
  blocks: InlineMediaContentBlock[]
  metadata: FileResult[]
  summary: string
}

/** Called only after semantic lookup. Core commits these inline bytes and input
 * atomically; this module never creates artifacts or marks a receipt accepted.
 * The caller holds work through serialization/delivery and releases it in finally.
 */
export async function prepareSlackFiles(
  client: SlackClient,
  files: readonly SlackEventFile[],
  context: OperationAttemptContext,
  work: ProviderWorkReservation,
): Promise<SlackInputFiles> {
  const blocks: InlineMediaContentBlock[] = []
  const metadata: FileResult[] = []
  let acceptedBytes = 0
  for (const [ordinal, original] of files.slice(0, maxFiles).entries()) {
    context.signal.throwIfAborted()
    let file = original
    let result = fileResult(file, ordinal)
    try {
      if (file.id && (file.file_access === 'check_file_info' || !privateURL(file))) {
        const hydrated = (await client.api('files.info', { file: file.id }, context)).file
        if (hydrated.id && hydrated.id !== file.id) throw new SlackAPIError('invalid_identity')
        file = mergeFile(file, hydrated)
        result = fileResult(file, ordinal)
      }
      const limit = Math.min(fileBytes, totalBytes - acceptedBytes)
      if (file.size > limit || limit <= 0) throw new SlackAPIError('too_large')
      const url = privateURL(file)
      if (!url) throw new SlackAPIError('missing_url')
      // Reserve before allocation: streamed chunks + joined bytes + base64,
      // JSON serialization and generated API validation can coexist temporarily.
      work.resize(acceptedBytes * 8 + 1024 * 1024)
      const downloaded = await client.download(url, limit, context, (bytes) => {
        // The provider's declared size is only a hint. Charge actual streamed
        // bytes before retaining them, including the later base64/JSON copies.
        work.resize((acceptedBytes + bytes) * 8 + 1024 * 1024)
      })
      if (downloaded.bytes.length === 0) throw new SlackAPIError('empty')
      const mediaType = attachmentMediaType(file, downloaded.contentType, downloaded.bytes)
      if (!mediaType) throw new SlackAPIError('unsupported_media_type')
      const filename = attachmentFilename(file, mediaType)
      blocks.push({
        type: 'media',
        media_type: mediaType,
        filename,
        data: downloaded.bytes.toString('base64'),
      })
      acceptedBytes += downloaded.bytes.length
      result = {
        ...result,
        status: 'stored',
        content_type: mediaType,
        filename,
        size_bytes: downloaded.bytes.length,
      }
    } catch (error) {
      context.signal.throwIfAborted()
      // Transient provider/download failures must replay the receipt, not silently
      // admit a message that permanently lost its files. Diagnostics exclude URLs.
      if (
        !(error instanceof SlackAPIError) ||
        error.retryable ||
        error.outcomeUnknown ||
        error.code === 'deadline_exceeded'
      )
        throw error
      result.reason = error.code
    } finally {
      work.resize(acceptedBytes * 8 + 1024 * 1024)
    }
    metadata.push(result)
  }
  if (files.length > maxFiles)
    metadata.push({
      status: 'skipped',
      reason: 'too_many_attachments',
      count: files.length - maxFiles,
    })
  const skipped = metadata
    .filter((file) => file.status === 'skipped')
    .map((file) => {
      const name = [file.filename, file.name, file.title, file.id].find(Boolean) ?? 'attachment'
      return `- ${name} skipped: ${(file.reason ?? 'unavailable').replaceAll('_', ' ')}`
    })
  return {
    blocks,
    metadata,
    summary: skipped.length ? `Slack files not included:\n${skipped.join('\n')}` : '',
  }
}

function fileResult(file: SlackEventFile, ordinal: number): FileResult {
  return {
    ordinal,
    id: file.id,
    name: file.name,
    title: file.title,
    mimetype: file.mimetype,
    declared_size_bytes: file.size,
    file_access: file.file_access,
    status: 'skipped',
  }
}
function mergeFile(original: SlackEventFile, hydrated: SlackEventFile): SlackEventFile {
  return {
    id: hydrated.id || original.id,
    name: hydrated.name || original.name,
    title: hydrated.title || original.title,
    mimetype: hydrated.mimetype || original.mimetype,
    size: hydrated.size || original.size,
    file_access: hydrated.file_access || original.file_access,
    url_private: hydrated.url_private || original.url_private,
    url_private_download: hydrated.url_private_download || original.url_private_download,
  }
}
function privateURL(file: SlackEventFile): string {
  return file.url_private_download.trim() || file.url_private.trim()
}
function typeByExtension(name: string): string {
  const extension = extname(name).toLowerCase()
  if (extension === '.jpeg') return 'image/jpeg'
  return Object.entries(extensions).find(([, value]) => value === extension)?.[0] ?? ''
}
function attachmentMediaType(
  file: SlackEventFile,
  header: string,
  bytes: Buffer,
): MediaType | undefined {
  const utf8 = isUtf8(bytes)
  const fileTypes = [typeByExtension(file.name), typeByExtension(file.title)]
  const candidates = [
    ...(utf8 ? fileTypes.filter((type) => type.startsWith('text/')) : []),
    file.mimetype,
    ...fileTypes,
    header,
    sniff(bytes),
  ]
  for (const candidate of candidates) {
    let value = candidate.split(';')[0]?.trim().toLowerCase()
    if (value === 'image/jpg') value = 'image/jpeg'
    if (value?.startsWith('text/') && !utf8) continue
    const parsed = schemas.zInlineMediaContentBlock.shape.media_type.safeParse(value)
    if (parsed.success) return parsed.data
  }
  return undefined
}
function sniff(bytes: Buffer): string {
  if (bytes.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])))
    return 'image/png'
  if (bytes.subarray(0, 3).equals(Buffer.from([255, 216, 255]))) return 'image/jpeg'
  const start = bytes.subarray(0, 12).toString('latin1')
  if (/^GIF8[79]a/.test(start)) return 'image/gif'
  if (start.startsWith('RIFF') && start.endsWith('WEBP')) return 'image/webp'
  if (start.startsWith('%PDF-')) return 'application/pdf'
  // Match net/http's plain-text sniff: prohibited control bytes identify binary.
  if (
    !bytes.subarray(0, 512).some((byte) => byte <= 8 || byte === 11 || (byte >= 14 && byte <= 31))
  )
    return 'text/plain'
  return ''
}
function attachmentFilename(file: SlackEventFile, type: MediaType): string {
  const name = file.name.trim() || file.title.trim() || file.id.trim()
  return !name || name.includes('\0') || Buffer.byteLength(name) > 255
    ? `attachment${extensions[type]}`
    : name
}
