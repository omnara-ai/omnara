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

  it('assembles a message from many chunks and returns several from one', () => {
    const parser = createServerSentEventParser()

    expect(parser.push('data: {"a"')).toEqual([])
    expect(parser.push(':1,"b"')).toEqual([])
    expect(parser.push(':2}\n')).toEqual([])
    expect(parser.push('\ndata: 3\n\ndata: 4\n\ndata: 5')).toEqual(['{"a":1,"b":2}', '3', '4'])
    expect(parser.push('\n\n')).toEqual(['5'])
  })

  it('treats CRLF and bare CR as line endings, including a CRLF split across chunks', () => {
    const parser = createServerSentEventParser()

    expect(parser.push('data: 1\r')).toEqual([])
    expect(parser.push('\n\r\ndata: 2\r\rdata: 3\r\n')).toEqual(['1', '2'])
    expect(parser.push('\r\n')).toEqual(['3'])
  })

  it('does not dispatch a message without data', () => {
    const parser = createServerSentEventParser()

    expect(parser.push('event: ping\nid: 3\n\n: keepalive\n\n')).toEqual([])
  })

  it('ignores empty chunks without losing a pending line ending', () => {
    const parser = createServerSentEventParser()

    expect(parser.push('data: x\r')).toEqual([])
    expect(parser.push('')).toEqual([])
    expect(parser.push('\n\n')).toEqual(['x'])
  })
})
