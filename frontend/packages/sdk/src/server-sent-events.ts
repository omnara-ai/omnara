export interface ServerSentEventParser {
  push(chunk: string): string[]
}

// Dispatch rules: https://html.spec.whatwg.org/multipage/server-sent-events.html#event-stream-interpretation
export function createServerSentEventParser(): ServerSentEventParser {
  // The message in progress is kept as chunks so a large frame is never rescanned or recopied.
  let parts: string[] = []
  let pendingCarriageReturn = false
  return {
    push(chunk) {
      if (chunk === '') return []
      // A CR that ended the previous chunk already counted as a line ending; drop the LF that pairs with it.
      let text = pendingCarriageReturn && chunk.startsWith('\n') ? chunk.slice(1) : chunk
      pendingCarriageReturn = chunk.endsWith('\r')
      text = text.replaceAll(/\r\n?/g, '\n')
      // A blank line can straddle chunks: move a buffered trailing LF into this chunk so one scan sees it.
      const last = parts.at(-1)
      if (last?.endsWith('\n') === true) {
        parts[parts.length - 1] = last.slice(0, -1)
        text = `\n${text}`
      }
      const messages: string[] = []
      let from = 0
      for (let end = text.indexOf('\n\n'); end !== -1; end = text.indexOf('\n\n', from)) {
        const data = messageData(parts.join('') + text.slice(from, end))
        parts = []
        if (data != null) messages.push(data)
        from = end + 2
      }
      if (from < text.length) parts.push(text.slice(from))
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
