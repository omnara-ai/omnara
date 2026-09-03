export const maxDiagnosticMessageBytes = 16 * 1024

const truncationMarker = '…'
const truncationMarkerBytes = 3

export function errorMessage(error: unknown): string {
  let value: string
  try {
    const message: unknown = error instanceof Error ? error.message : error
    value = String(message)
  } catch {
    value = 'unknown error'
  }
  return boundedDatabaseText(value)
}

export function boundedDatabaseText(value: string): string {
  const characters: { bytes: number; value: string }[] = []
  let bytes = 0
  let truncated = false
  for (const input of value) {
    const character = input === '\u0000' ? '\uFFFD' : input
    const characterBytes = utf8Bytes(character)
    if (bytes + characterBytes > maxDiagnosticMessageBytes) {
      truncated = true
      break
    }
    characters.push({ bytes: characterBytes, value: character })
    bytes += characterBytes
  }
  if (!truncated) return characters.map((character) => character.value).join('')
  while (bytes + truncationMarkerBytes > maxDiagnosticMessageBytes) {
    const removed = characters.pop()
    if (!removed) break
    bytes -= removed.bytes
  }
  return `${characters.map((character) => character.value).join('')}${truncationMarker}`
}

function utf8Bytes(value: string): number {
  const codePoint = value.codePointAt(0)
  if (codePoint === undefined || codePoint <= 0x7f) return 1
  if (codePoint <= 0x7ff) return 2
  if (codePoint <= 0xffff) return 3
  return 4
}
