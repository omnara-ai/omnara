export interface ServerSentEventParser {
  /** Consumes decoded text and returns the data of each message it completes, in order. */
  push(chunk: string): string[]
}

/**
 * Incremental `text/event-stream` parser: a message ends at a blank line,
 * `data` lines join with LF, comment lines and other fields are ignored, and a
 * message without data is not dispatched.
 */
export function createServerSentEventParser(): ServerSentEventParser {
  let buffer = ''
  return {
    push(chunk) {
      buffer += chunk
      // A trailing CR may be the first half of a CRLF pair split across chunks.
      const heldCarriageReturn = buffer.endsWith('\r')
      if (heldCarriageReturn) buffer = buffer.slice(0, -1)
      buffer = buffer.replaceAll(/\r\n?/g, '\n')
      const messages: string[] = []
      for (let end = buffer.indexOf('\n\n'); end !== -1; end = buffer.indexOf('\n\n')) {
        const data = messageData(buffer.slice(0, end))
        if (data != null) messages.push(data)
        buffer = buffer.slice(end + 2)
      }
      if (heldCarriageReturn) buffer += '\r'
      return messages
    },
  }
}

function messageData(block: string): string | undefined {
  const data: string[] = []
  for (const line of block.split('\n')) {
    if (line !== 'data' && !line.startsWith('data:')) continue
    const value = line.slice('data:'.length)
    data.push(value.startsWith(' ') ? value.slice(1) : value)
  }
  return data.length === 0 ? undefined : data.join('\n')
}
