import { decodeUTF8Text } from '@/lib/file-text'

export function memoryPreview(bytes: Uint8Array) {
  const starts = (...prefix: number[]) => prefix.every((value, index) => bytes[index] === value)
  if (starts(0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a))
    return { text: null, type: 'image/png' }
  if (starts(0xff, 0xd8, 0xff)) return { text: null, type: 'image/jpeg' }
  const signature = new TextDecoder().decode(bytes.subarray(0, 12))
  if (signature.startsWith('GIF87a') || signature.startsWith('GIF89a'))
    return { text: null, type: 'image/gif' }
  if (signature.startsWith('RIFF') && signature.slice(8) === 'WEBP')
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
