import { describe, expect, it } from 'vitest'

import { createServerSentEventParser } from './server-sent-events'

describe('createServerSentEventParser', () => {
  it('returns message data and ignores comments and other fields', () => {
    const parser = createServerSentEventParser()

    expect(
      parser.push(': heartbeat\n\nid: 7\nevent: model_output\nretry: 5000\ndata: {"a":1}\n\n'),
    ).toEqual(['{"a":1}'])
  })

  it('joins data lines with LF and strips one leading space per value', () => {
    const parser = createServerSentEventParser()

    expect(parser.push('data: first\ndata:second\ndata:  third\ndata\n\n')).toEqual([
      'first\nsecond\n third\n',
    ])
  })

  it('buffers a message across chunks and holds a trailing CR until it can be classified', () => {
    const parser = createServerSentEventParser()

    expect(parser.push('data: {"a"')).toEqual([])
    expect(parser.push(':1}\r')).toEqual([])
    // The CR that ended the previous chunk completes a CRLF here. The chunk's own
    // trailing CR could still be half of a CRLF, so message 2 is not dispatched yet.
    expect(parser.push('\n\r\ndata: 2\r\r')).toEqual(['{"a":1}'])
    // Bare CR line endings resolve once the next byte proves they were not CRLF.
    expect(parser.push('data: 3\n\n')).toEqual(['2', '3'])
  })

  it('does not dispatch a message without data', () => {
    const parser = createServerSentEventParser()

    expect(parser.push('event: ping\nid: 3\n\n: keepalive\n\n')).toEqual([])
  })

  it('keeps an incomplete trailing message buffered', () => {
    const parser = createServerSentEventParser()

    expect(parser.push('data: partial\n')).toEqual([])
    expect(parser.push('\n')).toEqual(['partial'])
  })
})
