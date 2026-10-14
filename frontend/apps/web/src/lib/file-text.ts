export function decodeUTF8Text(bytes: Uint8Array): string | null {
  try {
    const text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes)
    return text.includes('\0') ? null : text
  } catch {
    return null
  }
}
