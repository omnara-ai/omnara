import { describe, expect, it } from 'vitest'

import { event, receipt } from './behavior-test-support'
import { discordInputKey, discordInputText, parseDiscordReceipt } from './events'
import { config, thread } from './test-support'

describe('Discord saved native events', () => {
  it('preserves Markdown, native IDs and semantic identity across session replay', () => {
    const first = receipt()
    const replay = {
      ...first,
      event_id: 'discord:another-session:10',
      payload: { ...first.payload, s: 10 },
    }
    const parsed = parseDiscordReceipt(first)
    const again = parseDiscordReceipt(replay)
    if (!parsed || !again) throw new Error('expected message')
    expect(discordInputKey(parsed)).toBe(discordInputKey(again))
    expect(discordInputText(parsed, thread.id)[1]).toEqual({ type: 'text', text: event.content })
  })
  it.each([1, 6, 21, 32])('ignores unsupported native message type %s', (type) => {
    expect(parseDiscordReceipt(receipt({ type }))).toBeUndefined()
  })
  it('ignores DMs, webhooks and bot messages', () => {
    expect(parseDiscordReceipt(receipt({ guild_id: undefined }))).toBeUndefined()
    expect(parseDiscordReceipt(receipt({ webhook_id: config.applicationID }))).toBeUndefined()
    expect(parseDiscordReceipt(receipt({ author: { ...event.author, bot: true } }))).toBeUndefined()
  })
  it('rejects mismatched receipt sequence, malformed snowflakes and malformed ordinary content', () => {
    expect(() => parseDiscordReceipt({ ...receipt(), event_id: 'discord:session:3' })).toThrow(
      'invalid_event',
    )
    expect(() => parseDiscordReceipt(receipt({ channel_id: 'not-a-snowflake' }))).toThrow(
      'invalid_event',
    )
    expect(() =>
      parseDiscordReceipt({
        ...receipt(),
        payload: { op: 0, t: 'MESSAGE_CREATE', s: 2, d: { ...event, content: 123 } },
      }),
    ).toThrow('invalid_event')
  })
})
