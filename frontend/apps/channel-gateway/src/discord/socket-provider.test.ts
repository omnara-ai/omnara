import { describe, expect, it, onTestFinished, vi } from 'vitest'

import { discordIntents } from './bootstrap'
import { createDiscordSocket, type DiscordDispatch } from './socket'
import { socketProvider } from './socket-provider-test-support'
import { config } from './test-support'

describe('Discord installed SDK worker resources', () => {
  it('starts the published default worker, identifies and stops its real loopback socket', async () => {
    const provider = await socketProvider()
    const controller = new AbortController()
    const dispatches: DiscordDispatch[] = []
    const onFailure = vi.fn()
    const onFatalRuntimeFailure = vi.fn()
    const identify = {
      gatewayInfo: vi.fn().mockResolvedValue({
        url: provider.gatewayURL,
        shards: 4,
        session_start_limit: {
          total: 100,
          remaining: 50,
          reset_after: 60_000,
          max_concurrency: 2,
        },
      }),
      waitForIdentify: vi.fn().mockResolvedValue(undefined),
    }
    const socket = createDiscordSocket({
      api: provider.api,
      botToken: config.botToken,
      shard: { shard_id: 2, shard_count: 4 },
      signal: controller.signal,
      identify,
      stopTimeoutMs: 2000,
      retrieveSessionInfo: () => null,
      updateSessionInfo: vi.fn(),
      onDispatch: (payload) => {
        dispatches.push(payload)
      },
      onHeartbeat: vi.fn(),
      onFailure,
      onFatalRuntimeFailure,
      // Intentionally no buildStrategy: load the package's real defaultWorker.js.
    })
    onTestFinished(async () => {
      controller.abort()
      await socket.stop()
    })
    await socket.connect()
    await vi.waitFor(() => {
      expect(dispatches).toHaveLength(1)
    })
    expect(provider.identified).toEqual([
      expect.objectContaining({
        token: config.botToken,
        shard: [2, 4],
        intents: discordIntents,
      }),
    ])
    expect(identify.waitForIdentify).toHaveBeenCalledOnce()
    expect(dispatches[0]?.t).toBe('READY')
    await socket.stop()
    await vi.waitFor(() => {
      expect(provider.connections.size).toBe(0)
    })
    expect(provider.failures).toEqual([])
    expect(onFailure).not.toHaveBeenCalled()
    expect(onFatalRuntimeFailure).not.toHaveBeenCalled()
  }, 10_000)
})
