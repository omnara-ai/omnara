import { REST } from '@discordjs/rest'
import {
  type IShardingStrategy,
  type ManagerShardEventsMap,
  type SessionInfo,
  WebSocketManager,
  WebSocketShardEvents,
  WorkerShardingStrategy,
} from '@discordjs/ws'

import { raceWithAbort } from '../async'
import { discordIntents, validateDiscordGatewayURL } from './bootstrap'
import type { DiscordShardConfiguration } from './checkpoint'
import type { DiscordIdentify } from './identify'
import { DiscordAPIError } from './protocol'

export type DiscordDispatch = ManagerShardEventsMap[WebSocketShardEvents.Dispatch][0]
export interface DiscordSocketOptions {
  botToken: string
  api: URL
  shard: DiscordShardConfiguration
  signal: AbortSignal
  identify: Pick<DiscordIdentify, 'gatewayInfo' | 'waitForIdentify'>
  retrieveSessionInfo: (shardId: number) => SessionInfo | null
  updateSessionInfo: (shardId: number, session: SessionInfo | null) => void
  onDispatch: (payload: DiscordDispatch, shardId: number) => void
  onHeartbeat: (shardId: number) => void
  onFailure: (error: Error) => void
  onFatalRuntimeFailure: (error: Error) => void
  stopTimeoutMs: number
  /** Public strategy seam, used by transport fixtures. Production uses workers. */
  buildStrategy?: (manager: WebSocketManager) => IShardingStrategy
}
export interface DiscordSocket {
  connect(): Promise<void>
  stop(): Promise<void>
}

/** Public SDK hooks only. The worker strategy is essential: simple strategy
 * destroy does not cancel an already scheduled automatic reconnect timer.
 */
export function createDiscordSocket(options: DiscordSocketOptions): DiscordSocket {
  const lifetime = new AbortController()
  const signal = AbortSignal.any([options.signal, lifetime.signal])
  const guarded = (work: () => void): void => {
    if (signal.aborted) return
    try {
      work()
    } catch {
      options.onFailure(new DiscordAPIError('invalid_gateway_dispatch'))
    }
  }
  const sdkAPI = options.api.href.replace(/\/v10\/$/, '').replace(/\/$/, '')
  const rest = new REST({
    api: sdkAPI,
    retries: 0,
    timeout: 10_000,
    makeRequest: async (url, init) => {
      if (url !== `${sdkAPI}/v10/gateway/bot` || (init.method && init.method !== 'GET'))
        throw new DiscordAPIError('unexpected_gateway_metadata_request')
      const combined = init.signal ? AbortSignal.any([signal, init.signal]) : signal
      const info = await options.identify.gatewayInfo(combined)
      validateDiscordGatewayURL(info.url, options.api)
      const response = new Response(JSON.stringify(info), {
        headers: { 'content-type': 'application/json' },
      })
      // REST's public ResponseLike accepts a buffered response. Its Node stream
      // type differs from DOM fetch's stream; bind the actual body readers.
      return {
        status: response.status,
        statusText: response.statusText,
        ok: response.ok,
        headers: response.headers,
        body: null,
        get bodyUsed() {
          return response.bodyUsed
        },
        json: () => response.json(),
        text: () => response.text(),
        arrayBuffer: () => response.arrayBuffer(),
      }
    },
  }).setToken(options.botToken)
  const manager = new WebSocketManager({
    token: options.botToken,
    rest,
    intents: discordIntents,
    shardIds: [options.shard.shard_id],
    shardCount: options.shard.shard_count,
    handshakeTimeout: 10_000,
    helloTimeout: 10_000,
    readyTimeout: 10_000,
    buildStrategy: (value) =>
      guardedStrategy(
        options.buildStrategy?.(value) ?? new WorkerShardingStrategy(value, { shardsPerWorker: 1 }),
        signal,
      ),
    retrieveSessionInfo: options.retrieveSessionInfo,
    updateSessionInfo: (id, session) => {
      guarded(() => {
        options.updateSessionInfo(id, session)
      })
    },
    buildIdentifyThrottler: () => ({
      waitForIdentify: async (id, shardSignal) => {
        try {
          await options.identify.waitForIdentify(id, AbortSignal.any([signal, shardSignal]))
          signal.throwIfAborted()
        } catch (cause) {
          // This SDK's worker RPC swallows a rejected hook and waits forever
          // unless the owner explicitly stops the worker.
          const error =
            cause instanceof DiscordAPIError && cause.code === 'identify_startup_deferred'
              ? cause
              : new DiscordAPIError('identify_failed')
          options.onFailure(error)
          throw error
        }
      },
    }),
  })
  manager.on(WebSocketShardEvents.Dispatch, (payload, id) => {
    guarded(() => {
      options.onDispatch(payload, id)
    })
  })
  manager.on(WebSocketShardEvents.HeartbeatComplete, (_stats, id) => {
    guarded(() => {
      options.onHeartbeat(id)
    })
  })
  manager.on(WebSocketShardEvents.Error, () => {
    options.onFailure(new DiscordAPIError('gateway_error'))
  })
  manager.on(WebSocketShardEvents.SocketError, () => {
    /* SDK owns ordinary reconnect. */
  })
  manager.on(WebSocketShardEvents.Closed, (code) => {
    if ([4004, 4010, 4011, 4012, 4013, 4014].includes(code))
      options.onFailure(new DiscordAPIError('gateway_configuration_rejected'))
  })
  let stopping: Promise<void> | undefined
  return {
    connect: async () => {
      signal.throwIfAborted()
      await raceWithAbort(manager.connect(), signal)
    },
    stop: () => {
      if (stopping) return stopping
      lifetime.abort()
      stopping = (async () => {
        try {
          await raceWithAbort(
            Promise.resolve(manager.destroy({ reason: 'Discord capture owner stopped' })),
            AbortSignal.timeout(options.stopTimeoutMs),
          )
        } catch {
          const error = new DiscordAPIError('gateway_stop_failed')
          options.onFatalRuntimeFailure(error)
          throw error
        }
      })()
      return stopping
    },
  }
}

/** Do not let destroy miss a worker that is still starting. Compose the public
 * strategy interface; no access to SDK-private workers, maps or session state.
 */
function guardedStrategy(strategy: IShardingStrategy, signal: AbortSignal): IShardingStrategy {
  let spawning: Promise<void> = Promise.resolve()
  return {
    spawn: (ids) => {
      signal.throwIfAborted()
      spawning = Promise.resolve(strategy.spawn(ids))
      return spawning
    },
    connect: () => {
      signal.throwIfAborted()
      return strategy.connect()
    },
    destroy: async (options) => {
      await spawning.catch(() => undefined)
      await strategy.destroy(options)
    },
    send: (id, payload) => {
      signal.throwIfAborted()
      return strategy.send(id, payload)
    },
    fetchStatus: () => strategy.fetchStatus(),
  }
}
