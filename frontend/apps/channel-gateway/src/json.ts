import { isString } from './diagnostics'

/**
 * JSON.parse checks grammar; this bounded walk checks original member names
 * before duplicates can disappear and returns original top-level value slices.
 * It never represents payload numbers as JS numbers.
 */
export function parseObjectFields(raw: string, maxBytes: number): Map<string, string> {
  if (Buffer.byteLength(raw) > maxBytes || !raw.trim().startsWith('{'))
    throw new Error('invalid JSON object')
  try {
    JSON.parse(raw)
  } catch {
    throw new Error('invalid JSON object')
  }
  const fields = new Map<string, string>()
  let offset = 0
  let nodes = 0
  const whitespace = (): void => {
    while (/\s/.test(raw[offset] ?? '') && offset < raw.length) offset += 1
  }
  const stringEnd = (): number => {
    offset += 1
    while (offset < raw.length) {
      if (raw[offset] === '\\') offset += 2
      else if (raw[offset++] === '"') return offset
    }
    throw new Error('invalid JSON object')
  }
  const value = (depth: number): void => {
    if (depth > 128 || ++nodes > 16_384) throw new Error('invalid JSON object')
    whitespace()
    const start = raw[offset]
    if (start === '"') {
      stringEnd()
      return
    }
    if (start !== '{' && start !== '[') {
      while (offset < raw.length && !/[\s,}\]]/.test(raw[offset] ?? '')) offset += 1
      return
    }
    const object = start === '{'
    const close = object ? '}' : ']'
    const seen = new Set<string>()
    offset += 1
    whitespace()
    while (raw[offset] !== close) {
      let key = ''
      if (object) {
        const keyStart = offset
        const decoded: unknown = JSON.parse(raw.slice(keyStart, stringEnd()))
        if (!isString(decoded) || seen.has(decoded)) throw new Error('invalid JSON object')
        key = decoded
        seen.add(key)
        whitespace()
        offset += 1 // colon, already checked by JSON.parse
        whitespace()
      }
      const startOffset = offset
      value(depth + 1)
      if (object && depth === 0) fields.set(key, raw.slice(startOffset, offset))
      whitespace()
      if (raw[offset] === ',') {
        offset += 1
        whitespace()
      }
    }
    offset += 1
  }
  value(0)
  return fields
}
