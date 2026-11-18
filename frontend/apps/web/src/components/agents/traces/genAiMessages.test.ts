import { describe, expect, it } from 'vitest'

import { parseMessages } from './genAiMessages'

describe('parseMessages', () => {
  it('reads a GenAI message list', () => {
    const value = JSON.stringify([
      { role: 'user', parts: [{ type: 'text', content: 'hi' }] },
      { role: 'assistant', parts: [{ type: 'tool_call', name: 'web_search', arguments: {} }] },
    ])
    expect(parseMessages(value)?.map((message) => message.role)).toEqual(['user', 'assistant'])
  })

  it('leaves other values to be shown as they are', () => {
    expect(parseMessages('{"query":"otel"}')).toBeUndefined()
    expect(parseMessages('[{"kind":"spawned"}]')).toBeUndefined()
    expect(parseMessages('[not json')).toBeUndefined()
    expect(parseMessages('plain text')).toBeUndefined()
  })
})
