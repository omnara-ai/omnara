import { randomUUID } from 'node:crypto'

import { z } from 'zod'

import { abortableDelay, raceWithAbort } from '../async'
import type { RedisStateClient } from '../redis-client'
import { type DiscordGatewayInfo, gatewayInfoSchema } from './bootstrap'
import { type DiscordShardConfiguration, discordShardConfiguration } from './checkpoint'
import {
  commitIdentifyState,
  readIdentifyState,
  reserveIdentify,
  unlockIdentifyRefresh,
} from './identify-scripts'
import { DiscordAPIError, discordID } from './protocol'

const snapshotSchema = z.object({
  info: gatewayInfoSchema,
  remaining: z.number().int().nonnegative(),
  resetAt: z.number().nonnegative(),
  cachedUntil: z.number().nonnegative(),
})
const replySchema = z.union([
  z.tuple([z.literal('ok'), z.string()]),
  z.tuple([z.literal('wait'), z.string()]),
  z.tuple([z.literal('refresh')]),
  z.tuple([z.literal('granted')]),
  z.tuple([z.literal('exhausted')]),
  z.tuple([z.literal('startup_deferred')]),
])
export interface DiscordIdentifyOptions {
  redis: Pick<RedisStateClient, 'eval' | 'set'>
  applicationID: string
  fetchGatewayInfo(signal: AbortSignal): Promise<DiscordGatewayInfo>
  signal: AbortSignal
  shard: DiscordShardConfiguration
  initialReadySeen: () => boolean
  commandTimeoutMs?: number
  metadataCacheMs?: number
}

/** Shared across hosts, scoped to physical app identity rather than token/project.
 * Permits cannot fence arbitrarily delayed sockets. Never refund uncertain work.
 */
export class DiscordIdentify {
  private readonly keys: [string, string]
  private readonly prefix: string
  private readonly commandTimeoutMs: number
  private readonly cacheMs: number
  private readonly startupKey: string
  private publication: Promise<unknown> | undefined

  constructor(private readonly options: DiscordIdentifyOptions) {
    discordID.parse(options.applicationID)
    discordShardConfiguration.parse(options.shard)
    this.prefix = `omnara:discord:identify:{${options.applicationID}}`
    this.keys = [`${this.prefix}:state`, `${this.prefix}:refresh`]
    this.startupKey = `${this.prefix}:started:${options.shard.shard_count}`
    this.commandTimeoutMs = options.commandTimeoutMs ?? 5_000
    this.cacheMs = options.metadataCacheMs ?? 60_000
    if (
      ![this.commandTimeoutMs, this.cacheMs].every(
        (n) => Number.isSafeInteger(n) && n > 0 && n <= 2_147_483_647,
      )
    )
      throw new DiscordAPIError('invalid_identify_configuration')
  }

  /** Positive historical evidence only. An uncertain/late write stays true for
   * this exact topology. Retain the physical promise through command timeout so
   * quiet-owner heartbeat retries cannot accumulate unbounded Redis commands.
   */
  async publishReady(signal: AbortSignal): Promise<void> {
    if (!this.options.initialReadySeen()) return
    const combined = AbortSignal.any([this.options.signal, signal])
    combined.throwIfAborted()
    if (!this.publication) {
      const work = this.options.redis.eval("return redis.call('SETBIT',KEYS[1],ARGV[1],1)", {
        keys: [this.startupKey],
        arguments: [String(this.options.shard.shard_id)],
      })
      this.publication = work
      void work
        .finally(() => {
          if (this.publication === work) this.publication = undefined
        })
        .catch(() => undefined)
    }
    const work = this.publication
    await this.command(() => work, combined)
  }

  async gatewayInfo(signal: AbortSignal): Promise<DiscordGatewayInfo> {
    const combined = AbortSignal.any([this.options.signal, signal])
    for (;;) {
      const reply = replySchema.parse(
        await this.command(
          () =>
            this.options.redis.eval(readIdentifyState, {
              keys: this.keys,
              arguments: [],
            }),
          combined,
        ),
      )
      if (reply[0] === 'ok') return snapshotSchema.parse(JSON.parse(reply[1])).info
      if (reply[0] === 'wait') {
        await this.wait(Math.min(100, Number(reply[1])), combined)
        continue
      }
      if (reply[0] !== 'refresh') throw new DiscordAPIError('invalid_identify_state')
      const token = randomUUID()
      const locked = await this.command(
        () => this.options.redis.set(this.keys[1], token, { NX: true, PX: 15_000 }),
        combined,
      )
      if (!locked) continue
      try {
        const refreshSignal = AbortSignal.any([combined, AbortSignal.timeout(10_000)])
        const info = gatewayInfoSchema.parse(
          await raceWithAbort(this.options.fetchGatewayInfo(refreshSignal), refreshSignal),
        )
        await this.command(
          () =>
            this.options.redis.eval(commitIdentifyState, {
              keys: this.keys,
              arguments: [token, JSON.stringify(info), String(this.cacheMs)],
            }),
          combined,
        )
      } finally {
        // An uncertain command may have committed. The ownership token prevents
        // late cleanup from releasing a replacement refresher's lock.
        await this.command(
          () =>
            this.options.redis.eval(unlockIdentifyRefresh, {
              keys: [this.keys[1]],
              arguments: [token],
            }),
          combined,
        ).catch(() => undefined)
      }
    }
  }

  async waitForIdentify(shardId: number, signal: AbortSignal): Promise<void> {
    if (shardId !== this.options.shard.shard_id) throw new DiscordAPIError('invalid_shard')
    const combined = AbortSignal.any([this.options.signal, signal])
    for (;;) {
      const info = await this.gatewayInfo(combined)
      const concurrency = info.session_start_limit.max_concurrency
      const bucket = `${this.prefix}:bucket:${shardId % concurrency}`
      const reply = replySchema.parse(
        await this.command(
          () =>
            this.options.redis.eval(reserveIdentify, {
              keys: [...this.keys, bucket, this.startupKey],
              arguments: [
                String(concurrency),
                randomUUID(),
                '5100',
                String(shardId),
                this.options.initialReadySeen() ? '1' : '0',
              ],
            }),
          combined,
        ),
      )
      if (reply[0] === 'granted') {
        combined.throwIfAborted()
        return
      }
      if (reply[0] === 'exhausted') throw new DiscordAPIError('identify_budget_exhausted')
      if (reply[0] === 'startup_deferred') throw new DiscordAPIError('identify_startup_deferred')
      if (reply[0] === 'wait') await this.wait(Number(reply[1]), combined)
      else if (reply[0] !== 'refresh') throw new DiscordAPIError('invalid_identify_state')
    }
  }

  private async command<T>(work: () => Promise<T>, signal: AbortSignal): Promise<T> {
    signal.throwIfAborted()
    // Redis cancellation cannot roll back a submitted script. A late reservation
    // remains consumed; the caller's signal check prevents returning permission.
    return raceWithAbort(
      work(),
      AbortSignal.any([signal, AbortSignal.timeout(this.commandTimeoutMs)]),
    )
  }

  private async wait(milliseconds: number, signal: AbortSignal): Promise<void> {
    if (!Number.isFinite(milliseconds)) throw new DiscordAPIError('invalid_identify_state')
    await abortableDelay(Math.max(1, Math.min(milliseconds, 60_000)), signal)
    signal.throwIfAborted()
  }
}
