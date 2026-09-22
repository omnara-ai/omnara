import { ApiError } from '@omnara/sdk'

import { decodeUTF8Text } from '@/lib/file-text'

export function fileContentConflict(error: Error | null): ApiError | undefined {
  return error instanceof ApiError && error.code === 'file_content_conflict' ? error : undefined
}

export function memoryPreview(bytes: Uint8Array) {
  const starts = (...prefix: number[]) => prefix.every((value, index) => bytes[index] === value)
  if (starts(0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a))
    return { text: null, type: 'image/png' }
  if (starts(0xff, 0xd8, 0xff)) return { text: null, type: 'image/jpeg' }
  const signature = new TextDecoder().decode(bytes.subarray(0, 12))
  if (signature.startsWith('GIF87a') || signature.startsWith('GIF89a'))
    return { text: null, type: 'image/gif' }
  if (
    starts(0x52, 0x49, 0x46, 0x46) &&
    bytes[8] === 0x57 &&
    bytes[9] === 0x45 &&
    bytes[10] === 0x42 &&
    bytes[11] === 0x50
  )
    return { text: null, type: 'image/webp' }
  if (signature.startsWith('%PDF-')) return { text: null, type: 'application/pdf' }
  return { text: decodeUTF8Text(bytes), type: null }
}

export function downloadMemoryBlob(content: Blob, path: string) {
  const url = URL.createObjectURL(content)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = path.split('/').at(-1) ?? path
  anchor.click()
  setTimeout(() => {
    URL.revokeObjectURL(url)
  }, 1000)
}
