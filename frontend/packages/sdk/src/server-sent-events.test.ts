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

  it('completes a message whose CRLF pair is split across chunks', () => {
    const parser = createServerSentEventParser()

    expect(parser.push('data: {"a"')).toEqual([])
    expect(parser.push(':1}\r')).toEqual([])
    expect(parser.push('\n\r\n')).toEqual(['{"a":1}'])
  })

  it('treats a bare CR as a line ending once the next chunk shows it was not CRLF', () => {
    const parser = createServerSentEventParser()

    expect(parser.push('data: 2\r\r')).toEqual([])
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
