import type { AgentConfigModel, InlineMediaContentBlock } from '@omnara/sdk'

export const maxAttachmentBytes = 10 * 1024 * 1024
export const maxAttachmentCount = 20
export const maxTotalAttachmentBytes = 24 * 1024 * 1024

type AttachmentMediaType = InlineMediaContentBlock['media_type']

export interface SelectedAgentAttachment {
  id: string
  file: File
  kind: 'image' | 'document'
  mediaType: AttachmentMediaType
  data: string
}

const mediaByExtension = new Map<string, AttachmentMediaType>([
  ['csv', 'text/csv'],
  ['doc', 'application/msword'],
  ['dot', 'application/msword'],
  ['docx', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'],
  ['gif', 'image/gif'],
  ['iif', 'text/x-iif'],
  ['iwork', 'application/vnd.apple.iwork'],
  ['jpeg', 'image/jpeg'],
  ['jpg', 'image/jpeg'],
  ['key', 'application/vnd.apple.keynote'],
  ['markdown', 'text/markdown'],
  ['md', 'text/markdown'],
  ['odt', 'application/vnd.oasis.opendocument.text'],
  ['pages', 'application/vnd.apple.pages'],
  ['pdf', 'application/pdf'],
  ['png', 'image/png'],
  ['pot', 'application/vnd.ms-powerpoint'],
  ['ppa', 'application/vnd.ms-powerpoint'],
  ['pps', 'application/vnd.ms-powerpoint'],
  ['ppt', 'application/vnd.ms-powerpoint'],
  ['pptx', 'application/vnd.openxmlformats-officedocument.presentationml.presentation'],
  ['pwz', 'application/vnd.ms-powerpoint'],
  ['rtf', 'application/rtf'],
  ['tsv', 'text/tab-separated-values'],
  ['txt', 'text/plain'],
  ['webp', 'image/webp'],
  ['wiz', 'application/vnd.ms-powerpoint'],
  ['xla', 'application/vnd.ms-excel'],
  ['xlb', 'application/vnd.ms-excel'],
  ['xlc', 'application/vnd.ms-excel'],
  ['xlm', 'application/vnd.ms-excel'],
  ['xls', 'application/vnd.ms-excel'],
  ['xlsx', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'],
  ['xlt', 'application/vnd.ms-excel'],
  ['xlw', 'application/vnd.ms-excel'],
])

const mediaTypeAliases = new Map<string, AttachmentMediaType>([
  ['application/x-iif', 'text/x-iif'],
  ['image/jpg', 'image/jpeg'],
  ['text/rtf', 'application/rtf'],
  ['text/tsv', 'text/tab-separated-values'],
  ['text/x-markdown', 'text/markdown'],
])

function extension(filename: string): string {
  const index = filename.lastIndexOf('.')
  return index < 0 ? '' : filename.slice(index + 1).toLowerCase()
}

function allowsModality(model: AgentConfigModel, modality: string): boolean {
  return (
    model.input_modalities.length === 0 ||
    model.input_modalities.some((value) => value.trim().toLowerCase() === modality)
  )
}

function supportsAttachment(
  model: AgentConfigModel,
  kind: 'image' | 'document',
  mediaType: AttachmentMediaType,
): boolean {
  if (kind === 'image') return allowsModality(model, 'image')
  if (mediaType.startsWith('text/')) return allowsModality(model, 'text')
  if (model.api_format === 'openai-chat-completions' && mediaType === 'application/pdf') {
    return model.api_variant === 'openrouter' || allowsModality(model, 'file')
  }
  if (model.api_format === 'openai-responses' || mediaType === 'application/pdf') {
    return allowsModality(model, 'file')
  }
  return true
}

function isUTF8Text(bytes: Uint8Array): boolean {
  try {
    const text = new TextDecoder('utf-8', { fatal: true }).decode(bytes)
    return !text.includes('\0')
  } catch {
    return false
  }
}

export async function selectAgentAttachment(
  file: File,
  model: AgentConfigModel,
): Promise<SelectedAgentAttachment> {
  if (file.size === 0) throw new Error(`${file.name} is empty.`)
  if (file.size > maxAttachmentBytes) throw new Error(`${file.name} exceeds the 10 MB limit.`)
  if (Array.from(file.name).length > 255) {
    throw new Error(`${file.name} has a filename longer than 255 characters.`)
  }

  let bytes: Uint8Array | undefined
  async function readBytes() {
    bytes ??= new Uint8Array(await file.arrayBuffer())
    return bytes
  }

  const declaredType = file.type.toLowerCase().split(';', 1)[0]?.trim() ?? ''
  const normalizedType = mediaTypeAliases.get(declaredType) ?? declaredType
  let mediaType =
    mediaByExtension.get(extension(file.name)) ??
    [...mediaByExtension.values()].find((candidate) => candidate === normalizedType)
  if (mediaType == null) {
    if (!isUTF8Text(await readBytes())) {
      throw new Error(`${file.name} is not a supported image, document, or UTF-8 text file.`)
    }
    mediaType = 'text/plain'
  } else if (mediaType.startsWith('text/') && !isUTF8Text(await readBytes())) {
    throw new Error(`${file.name} is not valid UTF-8 text.`)
  }

  const kind = mediaType.startsWith('image/') ? 'image' : 'document'
  if (!supportsAttachment(model, kind, mediaType)) {
    throw new Error(`${file.name} is not supported by this agent's model.`)
  }
  if (kind === 'image' && model.api_format === 'anthropic-messages' && file.size > 7_500_000) {
    throw new Error(`${file.name} exceeds Anthropic's image limit.`)
  }

  return { id: crypto.randomUUID(), file, kind, mediaType, data: fileBase64(await readBytes()) }
}

function fileBase64(bytes: Uint8Array): string {
  let binary = ''
  for (let offset = 0; offset < bytes.length; offset += 32_768) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 32_768))
  }
  return btoa(binary)
}

export function attachmentSize(bytes: number): string {
  if (bytes < 1024) return `${String(bytes)} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}
