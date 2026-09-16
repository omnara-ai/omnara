import type { SessionInfo } from '@discordjs/ws'
import type { ChannelConnectorRuntimeUnit } from '@omnara/sdk'
import { z } from 'zod'

import type { RuntimeCheckpoint } from '../types'
import { validateDiscordGatewayURL } from './bootstrap'
import { DiscordAPIError } from './protocol'

/** Core atomically creates the app's units using one verified shard count. */
export const discordShardConfiguration = z
  .strictObject({
    shard_id: z.number().int().nonnegative(),
    shard_count: z.number().int().positive(),
  })
  .refine((value) => value.shard_id < value.shard_count)
export type DiscordShardConfiguration = z.infer<typeof discordShardConfiguration>
const sessionSchema: z.ZodType<SessionInfo> = z.strictObject({
  sessionId: z.string().min(1).max(256),
  sequence: z.number().int().nonnegative(),
  shardId: z.number().int().nonnegative(),
  shardCount: z.number().int().positive(),
  resumeURL: z.string().max(2048),
})
const checkpointSchema = discordShardConfiguration.safeExtend({
  initial_ready_seen: z.boolean(),
  session: sessionSchema.nullable(),
})

export interface DiscordCapturePosition {
  readonly epoch: number
  readonly session: Readonly<SessionInfo>
}

/** SDK update hooks precede Dispatch. Observed sequence numbers are never safe
 * resume points until serial capture acknowledges the corresponding dispatch.
 */
export class DiscordCheckpoint {
  private durable: SessionInfo | null = null
  private observed: SessionInfo | null = null
  private epoch = 0
  private stopped = false
  private initialized = false

  get initialReadySeen(): boolean {
    return this.initialized
  }

  constructor(
    readonly shard: DiscordShardConfiguration,
    unit: Pick<ChannelConnectorRuntimeUnit, 'checkpoint' | 'checkpoint_version'>,
    private readonly api: URL,
    private readonly persist: (checkpoint: RuntimeCheckpoint) => void,
  ) {
    if (!Object.keys(unit.checkpoint).length) return
    if (unit.checkpoint_version !== 1) throw new DiscordAPIError('unsupported_checkpoint')
    const data = checkpointSchema.parse(unit.checkpoint)
    if (data.shard_id !== shard.shard_id || data.shard_count !== shard.shard_count) return
    const saved = data.session
    if (saved && !this.matchesShard(saved)) return
    this.initialized = data.initial_ready_seen
    if (saved) {
      validateDiscordGatewayURL(saved.resumeURL, api)
      this.durable = { ...saved }
      this.observed = { ...saved }
    }
  }

  retrieve = (shardId: number): SessionInfo | null => {
    this.requireShard(shardId)
    return this.durable ? { ...this.durable } : null
  }

  observe = (shardId: number, value: SessionInfo | null): void => {
    this.requireShard(shardId)
    if (this.stopped) return
    if (value === null) {
      this.epoch++
      this.observed = null
      this.durable = null
      this.save()
      return
    }
    const session = sessionSchema.parse(value)
    if (!this.matchesShard(session)) throw new DiscordAPIError('session_shard_mismatch')
    validateDiscordGatewayURL(session.resumeURL, this.api)
    if (this.observed?.sessionId !== session.sessionId) {
      this.epoch++
      this.durable = null
    }
    this.observed = { ...session }
  }

  position(sequence: number): DiscordCapturePosition {
    if (!this.observed || !Number.isSafeInteger(sequence) || sequence < 0)
      throw new DiscordAPIError('missing_capture_session')
    return { epoch: this.epoch, session: { ...this.observed, sequence } }
  }

  captured(position: DiscordCapturePosition, verifiedReady = false): void {
    if (this.stopped || position.epoch !== this.epoch) return
    // Only the serial runtime's identity-validated READY may establish this fact.
    // SDK observation and Identify permits are not startup evidence.
    const newlyReady = verifiedReady && !this.initialized
    if (verifiedReady) this.initialized = true
    if (this.durable && position.session.sequence <= this.durable.sequence) {
      if (newlyReady) this.save()
      return
    }
    this.durable = { ...position.session }
    this.save()
  }

  stop(): void {
    this.stopped = true
  }

  private save(): void {
    this.persist({
      version: 1,
      checkpoint: {
        ...this.shard,
        initial_ready_seen: this.initialized,
        session: this.durable ? { ...this.durable } : null,
      },
    })
  }

  private requireShard(id: number): void {
    if (id !== this.shard.shard_id) throw new DiscordAPIError('session_shard_mismatch')
  }

  private matchesShard(session: SessionInfo): boolean {
    return session.shardId === this.shard.shard_id && session.shardCount === this.shard.shard_count
  }
}
