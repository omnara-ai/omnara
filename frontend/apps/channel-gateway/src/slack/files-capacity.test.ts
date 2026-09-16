import { afterEach, describe, expect, it, vi } from 'vitest'

import { loadConfig } from '../config'
import { operationWorkBytes } from '../operations'
import { initialReceiptWorkBytes, receiptFetch } from '../receipt-http'
import { GatewayAtCapacityError, WorkByteBudget } from '../work-budget'
import { SlackClient } from './client'
import { prepareSlackFiles } from './files'
import { slackEventFile } from './protocol'
import { attempt } from './test-support'

afterEach(() => {
  vi.unstubAllGlobals()
})

const mib = 1024 * 1024
function defaultBudget() {
  const config = loadConfig({
    OMNARA_CHANNEL_CORE_API_URL: 'http://api.invalid',
    OMNARA_CHANNEL_CONNECTOR_TOKEN: 'test-token',
    OMNARA_CHANNEL_GATEWAY_PUBLIC_URL: 'https://gateway.invalid',
  })
  return new WorkByteBudget(config.webhookMaxBufferedBytes)
}

function streamFile(bytes: number) {
  return new Response(
    new ReadableStream<Uint8Array>({
      pull(controller) {
        if (!bytes) {
          controller.close()
          return
        }
        const chunk = Math.min(bytes, 64 * 1024)
        bytes -= chunk
        controller.enqueue(new Uint8Array(chunk))
      },
    }),
    { headers: { 'content-type': 'application/pdf' } },
  )
}

async function claimWork(budget: WorkByteBudget, count: number) {
  const reservations = Array.from({ length: count }, () => budget.reserve(initialReceiptWorkBytes))
  await Promise.all(
    reservations.map(async (work) => {
      const fetch = receiptFetch(
        () => Promise.resolve(Response.json({ payload: { event: { text: 'hello' } } })),
        24 * mib,
        new AbortController().signal,
        work,
      )
      await (await fetch('http://core.invalid')).json()
    }),
  )
  return reservations
}

describe('default gateway media capacity', () => {
  it('cancels a stream on actual capacity exhaustion and releases partial bytes for retry', async () => {
    const budget = new WorkByteBudget(2 * mib)
    const work = budget.reserve(0)
    const canceled = vi.fn()
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>(() =>
        Promise.resolve(
          new Response(
            new ReadableStream<Uint8Array>({
              pull(controller) {
                controller.enqueue(new Uint8Array(64 * 1024))
              },
              cancel: canceled,
            }),
          ),
        ),
      ),
    )
    try {
      await expect(
        prepareSlackFiles(
          new SlackClient('test-token'),
          [slackEventFile.parse({ id: 'F1', size: 1, url_private: 'https://files.slack.com/F1' })],
          attempt(),
          work,
        ),
      ).rejects.toBeInstanceOf(GatewayAtCapacityError)
      expect(canceled).toHaveBeenCalledOnce()
      expect(budget.usedBytes).toBe(mib)
    } finally {
      work.release()
    }
    expect(budget.usedBytes).toBe(0)
  })

  it('fits four small receipt workers with files alongside an operation', async () => {
    const budget = defaultBudget()
    const claims = await claimWork(budget, 4)
    expect(budget.usedBytes).toBeLessThan(mib)
    const operation = budget.reserve(operationWorkBytes)
    const media = Array.from({ length: 4 }, () => budget.reserve(0))
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>(() => Promise.resolve(streamFile(1024))),
    )
    try {
      await Promise.all(
        media.map(async (work) => {
          const files = await prepareSlackFiles(
            new SlackClient('test-token'),
            [
              slackEventFile.parse({
                id: 'F1',
                size: 1,
                url_private: 'https://files.slack.com/F1',
              }),
            ],
            attempt(),
            work,
          )
          expect(files.blocks).toHaveLength(1)
          expect(files.metadata[0]?.size_bytes).toBe(1024)
        }),
      )
      expect(budget.usedBytes).toBeLessThan(operationWorkBytes + 6 * mib)
    } finally {
      for (const work of [...claims, ...media, operation]) work.release()
    }
    expect(budget.usedBytes).toBe(0)
  })

  it('fits the full 24 MiB input through serialization with understated or absent sizes', async () => {
    const budget = defaultBudget()
    const claims = await claimWork(budget, 4)
    // Include conservative headroom for the maximum normal Slack event.
    claims[0]?.resize(32 * mib)
    const operation = budget.reserve(operationWorkBytes)
    const work = budget.reserve(0)
    const sizes = [10 * mib, 10 * mib, 4 * mib]
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>(() => Promise.resolve(streamFile(sizes.shift() ?? 0))),
    )
    try {
      const files = await prepareSlackFiles(
        new SlackClient('test-token'),
        Array.from({ length: 3 }, (_, index) =>
          slackEventFile.parse({
            id: `F${index}`,
            size: index === 0 ? 1 : undefined,
            url_private: `https://files.slack.com/F${index}`,
          }),
        ),
        attempt(),
        work,
      )
      expect(files.metadata.map((file) => file.size_bytes)).toEqual([10 * mib, 10 * mib, 4 * mib])
      const serialized = JSON.stringify({ content_blocks: files.blocks })
      expect(Buffer.byteLength(serialized)).toBeGreaterThan(32 * mib)
      expect(budget.usedBytes).toBeLessThanOrEqual(budget.limitBytes)
      expect(files.summary).toBe('')
    } finally {
      for (const reservation of [...claims, operation, work]) reservation.release()
    }
    expect(budget.usedBytes).toBe(0)
  })
})
