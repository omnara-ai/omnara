import { describe, expect, it, vi } from 'vitest'

import { fetchBoundedMedia, messageContentBlocks } from './chat-sdk-media'
import { testMessage } from './gateway-test-fixtures'

describe('bounded Chat SDK media', () => {
  it('rejects remote attachments without a declared size before downloading', async () => {
    const fetchData = vi.fn(() => Promise.resolve(Buffer.from([1, 2, 3])))
    const message = testMessage({
      attachments: [{ fetchData, type: 'image', mimeType: 'image/png', name: 'image.png' }],
      text: '',
    })

    await expect(messageContentBlocks(message, 10, 10)).rejects.toThrow('declare its size')
    expect(fetchData).not.toHaveBeenCalled()
  })

  it('bounds multibyte inbound text before retaining or submitting it', async () => {
    const message = testMessage({
      attachments: [],
      text: '界'.repeat(Math.floor((1024 * 1024) / 3) + 1),
    })
    const reserveWorkBytes = vi.fn(noopWorkReservation)

    await expect(messageContentBlocks(message, 10, 20, { reserveWorkBytes })).rejects.toThrow(
      'text exceeds its byte limit',
    )
    expect(reserveWorkBytes).not.toHaveBeenCalled()
  })

  it('reserves serialization headroom for accepted inbound text', async () => {
    const reserveWorkBytes = vi.fn(noopWorkReservation)
    const message = testMessage({ attachments: [], text: '界' })

    await expect(messageContentBlocks(message, 10, 20, { reserveWorkBytes })).resolves.toEqual([
      { text: '界', type: 'text' },
    ])
    expect(reserveWorkBytes).toHaveBeenCalledWith(9)
  })

  it('rejects an oversized Blob before allocating its bytes', async () => {
    const blob = new Blob([new Uint8Array(11)])
    const arrayBuffer = vi.spyOn(blob, 'arrayBuffer')
    const message = testMessage({
      attachments: [{ data: blob, type: 'image', mimeType: 'image/png', name: 'image.png' }],
      text: '',
    })

    await expect(messageContentBlocks(message, 10, 20)).rejects.toThrow('per-item byte limit')
    expect(arrayBuffer).not.toHaveBeenCalled()
  })

  it('bounds content blocks and reports deterministically omitted attachments', async () => {
    const message = testMessage({
      attachments: Array.from({ length: 101 }, (_, index) => ({
        type: 'image',
        mimeType: 'application/x-unsupported',
        name: `image-${index}.png`,
      })),
      text: '',
    })

    const blocks = await messageContentBlocks(message, 10, 200)

    expect(blocks).toHaveLength(100)
    expect(blocks.at(-1)).toEqual({
      text: '[2 additional channel attachments omitted]',
      type: 'text',
    })
  })

  it('bounds media items before downloading provider attachments', async () => {
    const fetchData = vi.fn(() => Promise.resolve(Buffer.from([1])))
    const message = testMessage({
      attachments: Array.from({ length: 25 }, (_, index) => ({
        fetchData,
        type: 'image',
        mimeType: 'image/png',
        name: `image-${index}.png`,
        size: 1,
      })),
      text: '',
    })

    const blocks = await messageContentBlocks(message, 10, 200, {
      fetchAttachmentData: (attachment) => {
        if (!attachment.fetchData) throw new Error('missing test attachment loader')
        return attachment.fetchData()
      },
    })

    expect(blocks).toHaveLength(21)
    expect(fetchData).toHaveBeenCalledTimes(20)
    expect(blocks.at(-1)).toEqual({
      text: '[5 additional channel attachments omitted]',
      type: 'text',
    })
  })

  it('normalizes standard ArrayBuffer attachment data from Chat SDK adapters', async () => {
    const message = testMessage({
      attachments: [
        {
          fetchData: () => Promise.resolve(Uint8Array.from([1, 2, 3]).buffer),
          type: 'image',
          mimeType: 'image/png',
          name: 'image.png',
          size: 3,
        },
      ],
      text: '',
    })

    await expect(
      messageContentBlocks(message, 10, 20, {
        fetchAttachmentData: (attachment) => {
          if (!attachment.fetchData) throw new Error('missing test attachment loader')
          return attachment.fetchData()
        },
      }),
    ).resolves.toEqual([
      { data: 'AQID', filename: 'image.png', media_type: 'image/png', type: 'media' },
    ])
  })

  it('preflights Buffer bytes and bounds provider filenames to the core schema', async () => {
    const oversized = testMessage({
      attachments: [
        { data: Buffer.alloc(11), type: 'image', mimeType: 'image/png', name: 'oversized.png' },
      ],
      text: '',
    })
    await expect(messageContentBlocks(oversized, 10, 20)).rejects.toThrow('per-item byte limit')

    const longName = `${'🖼️'.repeat(260)}.png`
    const bounded = testMessage({
      attachments: [
        { data: Buffer.from([1]), type: 'image', mimeType: 'image/png', name: longName },
      ],
      text: '',
    })
    const [block] = await messageContentBlocks(bounded, 10, 20)

    expect(block?.type).toBe('media')
    if (block?.type !== 'media') throw new Error('expected media block')
    expect(Buffer.byteLength(block.filename ?? '', 'utf8')).toBeLessThanOrEqual(255)
    expect(longName.startsWith(block.filename ?? '')).toBe(true)
  })

  it('aborts a hung provider media loader with the inbound signal', async () => {
    const controller = new AbortController()
    let loaderSignal: AbortSignal | undefined
    const message = testMessage({
      attachments: [
        {
          fetchData: () => Promise.resolve(Buffer.from([1])),
          type: 'image',
          mimeType: 'image/png',
          name: 'image.png',
          size: 1,
        },
      ],
      text: '',
    })
    const loading = messageContentBlocks(message, 10, 20, {
      fetchAttachmentData: (_attachment, context) => {
        loaderSignal = context.signal
        return new Promise<Buffer>(() => undefined)
      },
      fetchTimeoutMs: 1_000,
      signal: controller.signal,
    })
    await vi.waitFor(() => {
      expect(loaderSignal).toBeDefined()
    })

    controller.abort(new Error('webhook deadline reached'))
    await expect(loading).rejects.toThrow('webhook deadline reached')
    expect(loaderSignal?.aborted).toBe(true)
  })

  it('stops a provider media stream as soon as its byte bound is crossed', async () => {
    let canceled = false
    const fetchMock = vi.fn(() =>
      Promise.resolve(
        new Response(
          new ReadableStream({
            cancel: () => {
              canceled = true
            },
            start(controller) {
              controller.enqueue(Uint8Array.from([1, 2, 3]))
              controller.enqueue(Uint8Array.from([4, 5, 6]))
            },
          }),
        ),
      ),
    )
    vi.stubGlobal('fetch', fetchMock)
    try {
      await expect(
        fetchBoundedMedia(
          'https://provider.example/media',
          {},
          {
            maxBytes: 5,
            signal: new AbortController().signal,
          },
        ),
      ).rejects.toThrow('bounded media byte limit')
      await vi.waitFor(() => {
        expect(canceled).toBe(true)
      })
    } finally {
      vi.unstubAllGlobals()
    }
  })
})

function noopWorkReservation() {
  return { release: () => undefined, resize: () => undefined }
}
