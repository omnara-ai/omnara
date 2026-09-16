import { afterEach, describe, expect, it, vi } from 'vitest'

import { ReceiptBehaviorError } from '../types'
import { processDiscordEvent } from './behavior'
import { event, fixture, inputKey, receipt } from './behavior-test-support'
import { config, json, room } from './test-support'

const attachment = {
  id: '888888888888888888',
  filename: 'notes.txt',
  size: 1,
  content_type: 'text/plain',
  url: `https://cdn.discordapp.com/attachments/${room.id}/888888888888888888/notes.txt?ex=old`,
}
const current = { ...attachment, url: attachment.url.replace('ex=old', 'ex=fresh') }
afterEach(() => {
  vi.unstubAllGlobals()
})

function requestURL(input: RequestInfo | URL): string {
  return input instanceof Request ? input.url : input.toString()
}
function mediaFetch(response: (init?: RequestInit) => Response | Promise<Response>) {
  const original = globalThis.fetch
  const fetch = vi.fn<typeof globalThis.fetch>((input, init) => {
    if (requestURL(input).startsWith('https://cdn.discordapp.com/')) {
      expect(init?.headers).toBeUndefined() // No bot token leaks to CDN.
      expect(init?.redirect).toBe('error')
      return Promise.resolve(response(init))
    }
    return original(input, init)
  })
  vi.stubGlobal('fetch', fetch)
  return fetch
}

async function withFile() {
  const f = await fixture((request, response) => {
    if (request.url === `/channels/${room.id}/messages/${event.id}`) {
      json(response, { ...event, content: 'edited after capture', attachments: [current] })
      return true
    }
    return false
  })
  const reserve = vi.fn(f.context.reserveWorkBytes)
  f.context.reserveWorkBytes = reserve
  return { ...f, reserve }
}

describe('Discord inbound attachments', () => {
  it('refreshes signed URLs after semantic lookup, preserves captured text and charges actual bytes', async () => {
    const f = await withFile()
    let peak = 0
    const nativeReserve = f.context.reserveWorkBytes
    f.context.reserveWorkBytes = (bytes) => {
      const work = nativeReserve(bytes)
      return {
        release: work.release,
        resize: (size) => {
          peak = Math.max(peak, size)
          work.resize(size)
        },
      }
    }
    const fetch = mediaFetch(() => {
      expect(f.core.lookupRecipients).toHaveBeenCalledOnce()
      expect(f.core.lookupWorkflow).toHaveBeenCalledOnce()
      return new Response('actual bytes exceed declared size')
    })
    await processDiscordEvent(receipt({ attachments: [attachment] }), f.context)
    const body = f.core.deliverWorkflow.mock.calls[0]?.[1]
    expect(body?.content_blocks).toContainEqual({ type: 'text', text: event.content })
    expect(body?.content_blocks).toContainEqual({
      type: 'media',
      media_type: 'text/plain',
      filename: 'notes.txt',
      data: Buffer.from('actual bytes exceed declared size').toString('base64'),
    })
    expect(fetch.mock.calls.some(([url]) => requestURL(url) === current.url)).toBe(true)
    expect(peak).toBeGreaterThan(1024 * 1024 + 8)
    expect(f.budget.usedBytes).toBe(0)
  })

  it('skips all attachment reads on semantic replay', async () => {
    const f = await withFile()
    f.core.lookupWorkflow.mockResolvedValue({
      exists: true,
      agent_state: 'active',
      input_keys: [inputKey],
    })
    const fetch = mediaFetch(() => {
      throw new Error('must not download')
    })
    await processDiscordEvent(receipt({ attachments: [attachment] }), f.context)
    expect(fetch.mock.calls.some(([url]) => requestURL(url) === current.url)).toBe(false)
    expect(f.calls.some((call) => call.endsWith(`/messages/${event.id}`))).toBe(false)
    expect(f.budget.usedBytes).toBe(0)
  })

  it.each(['oversized', 'invalid_utf8', 'unsupported'])(
    'reports omitted %s bytes without pretending attachment admission',
    async (kind) => {
      const f = await withFile()
      mediaFetch(() =>
        kind === 'oversized'
          ? new Response(new Uint8Array(10 * 1024 * 1024 + 1))
          : new Response(new Uint8Array([0xff])),
      )
      await processDiscordEvent(
        receipt({
          attachments: [
            { ...attachment, content_type: kind === 'unsupported' ? 'video/mp4' : 'text/plain' },
          ],
        }),
        f.context,
      )
      const blocks = f.core.deliverWorkflow.mock.calls[0]?.[1].content_blocks ?? []
      expect(blocks.some((block) => block.type === 'media')).toBe(false)
      expect(
        blocks.some(
          (block) => block.type === 'text' && block.text.startsWith('[Discord attachment'),
        ),
      ).toBe(true)
      expect(f.budget.usedBytes).toBe(0)
    },
  )

  it('retries transient download failure rather than admitting missing media', async () => {
    const f = await withFile()
    mediaFetch(() => new Response('', { status: 503 }))
    await expect(
      processDiscordEvent(receipt({ attachments: [attachment] }), f.context),
    ).rejects.toEqual(new ReceiptBehaviorError(true))
    expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
    expect(f.budget.usedBytes).toBe(0)
  })

  it('rejects cross-channel attachment URLs without fetching them', async () => {
    const f = await fixture((request, response) => {
      if (request.url === `/channels/${room.id}/messages/${event.id}`) {
        json(response, {
          ...event,
          attachments: [{ ...current, url: current.url.replace(room.id, config.guildID) }],
        })
        return true
      }
      return false
    })
    const fetch = mediaFetch(() => {
      throw new Error('must not fetch')
    })
    await processDiscordEvent(receipt({ attachments: [attachment] }), f.context)
    expect(
      fetch.mock.calls.some(([url]) => requestURL(url).startsWith('https://cdn.discordapp.com/')),
    ).toBe(false)
  })

  it('passes receipt interruption to the active media fetch and releases its bytes', async () => {
    const f = await withFile()
    const controller = new AbortController()
    f.context.signal = controller.signal
    let downloadSignal: AbortSignal | null | undefined
    mediaFetch((init) => {
      downloadSignal = init?.signal
      return new Response(
        new ReadableStream({
          start(stream) {
            stream.enqueue(new TextEncoder().encode('partial'))
            downloadSignal?.addEventListener(
              'abort',
              () => {
                stream.error(new Error('aborted'))
              },
              { once: true },
            )
          },
        }),
      )
    })
    const running = processDiscordEvent(receipt({ attachments: [attachment] }), f.context)
    const rejected = expect(running).rejects.toEqual(new ReceiptBehaviorError(true))
    await vi.waitFor(() => {
      expect(f.budget.usedBytes).toBeGreaterThan(0)
    })
    controller.abort()
    await rejected
    expect(downloadSignal?.aborted).toBe(true)
    expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
    expect(f.budget.usedBytes).toBe(0)
  })
})
