import { describe, expect, it } from 'vitest'

import { parseSlackMessage } from './messages'

describe('Slack history text', () => {
  it.each([
    ['`x*y*z`', '`x*y*z`'],
    ['snake_case_name', 'snake_case_name'],
    ['<@U123|bob>', '<@U123> (bob)'],
    ['<@U123>', '<@U123>'],
    ['<#C123|general>', '<#C123> (#general)'],
    ['```\nx*y*z &lt; value\n```', '```\nx*y*z < value\n```'],
  ])('preserves provider text and identity: %s', (text, expected) => {
    expect(parseSlackMessage({ ts: '1.000001', text, user: 'U123' })).toMatchObject({
      timestamp: '1.000001',
      authorRef: 'U123',
      text: expected,
      partial: false,
    })
  })

  it('retains native file IDs and marks unrepresented content partial', () => {
    expect(
      parseSlackMessage({
        ts: '1.000001',
        text: '',
        bot_id: 'B1',
        files: [{ id: 'F1', name: 'notes.md' }],
        blocks: [{}],
      }),
    ).toEqual({
      timestamp: '1.000001',
      text: '',
      authorRef: 'B1',
      files: [{ id: 'F1', name: 'notes.md' }],
      partial: true,
    })
  })
})
