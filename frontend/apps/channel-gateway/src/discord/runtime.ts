import type { ChannelConnectorRuntimeUnit } from '@omnara/sdk'
import { schemas } from '@omnara/sdk'
import { z } from 'zod'

import { raceWithAbort } from '../async'
import { isCoreNotFoundError } from '../core/requests'
import type { ProviderFactoryContext, RuntimeUnitContext } from '../types'
import { WorkReservationScope } from '../work-budget'
import { discordAPIURL, fetchDiscordGatewayInfo, inspectDiscordApplication } from './bootstrap'
import {
  type DiscordCapturePosition,
  DiscordCheckpoint,
  discordShardConfiguration,
} from './checkpoint'
import { DiscordIdentify, type DiscordIdentifyOptions } from './identify'
import { DiscordAPIError, discordID } from './protocol'
import {
  createDiscordSocket,
  type DiscordDispatch,
  type DiscordSocket,
  type DiscordSocketOptions,
} from './socket'

export interface DiscordRuntimeOptions {
  redis: DiscordIdentifyOptions['redis']
  onFatalRuntimeFailure: (error: Error) => void
  apiUrl?: string
  stopTimeoutMs: number
  /** Inject only the socket transport in tests; capture/checkpoint logic is real. */
  createSocket?: (options: DiscordSocketOptions) => DiscordSocket
}
const guildEvent = z.object({ guild_id: discordID.optional() })
const readyEvent = z.object({
  application: z.object({ id: discordID }),
  user: z.object({ id: discordID }),
})
const maxCaptureBytes = 24 * 1024 * 1024 // Existing durable inbox payload limit.
interface Capture {
  payload: DiscordDispatch
  position: DiscordCapturePosition
}

/** One leased shard, no workflow/agent work before the durable inbox ACK. */
export async function runDiscordUnit(
  factory: ProviderFactoryContext,
  unit: ChannelConnectorRuntimeUnit,
  context: RuntimeUnitContext,
  options: DiscordRuntimeOptions,
): Promise<void> {
  const shard = discordShardConfiguration.parse(unit.configuration)
  if (
    unit.runtime_kind !== 'discord_gateway' ||
    unit.unit_key !== `discord_gateway:${shard.shard_id}` ||
    unit.integration_app_id !== factory.configuration.app.id ||
    unit.integration_install_id !== undefined ||
    unit.lease_app_configuration_revision !== factory.configuration.app.configuration_revision ||
    unit.lease_spec_revision !== unit.spec_revision
  )
    throw new DiscordAPIError('runtime_scope_mismatch')
  const lifetime = new AbortController()
  const signal = AbortSignal.any([context.signal, factory.signal, lifetime.signal])
  const reservations = new WorkReservationScope(context.reserveWorkBytes)
  const api = discordAPIURL(options.apiUrl)
  const checkpoint = new DiscordCheckpoint(shard, unit, api, context.updateCheckpoint)
  let failure: Error | undefined
  let socket: DiscordSocket | undefined
  let draining: Promise<void> = Promise.resolve()
  const fail = (error: Error): void => {
    if (signal.aborted) return
    failure = error
    lifetime.abort(error)
  }
  const identity = await inspectDiscordApplication(
    factory.configuration,
    {
      requestId: unit.id,
      deadlineMs: Date.now() + 10_000,
      signal,
    },
    options.apiUrl,
  )
  const identify = new DiscordIdentify({
    redis: options.redis,
    applicationID: identity.applicationID,
    signal,
    shard,
    initialReadySeen: () => checkpoint.initialReadySeen,
    fetchGatewayInfo: (refreshSignal) =>
      fetchDiscordGatewayInfo(identity.botToken, api, {
        requestId: unit.id,
        deadlineMs: Date.now() + 10_000,
        attempt: 1,
        signal: refreshSignal,
      }),
  })

  let announcing: Promise<void> | undefined
  const announceReady = (): void => {
    if (signal.aborted || announcing || !checkpoint.initialReadySeen) return
    announcing = identify
      .publishReady(signal)
      .catch(() => {
        // Capture can continue while Redis is unavailable. New Identify still
        // fails closed; a later public heartbeat republishes this positive fact.
        if (!signal.aborted) factory.logger.warn('Discord startup evidence publication failed')
      })
      .finally(() => {
        announcing = undefined
      })
  }
  // Reconstruct before any reconnect attempt, even if this shard currently has
  // no resumable session or its provider connection fails again.
  announceReady()

  const capture = async (item: Capture): Promise<void> => {
    if (signal.aborted) return
    // An invalidated session cannot resume these already-received messages.
    // Save them with their original receipt identity; captured() alone fences
    // old epochs from advancing the replacement session's checkpoint.
    const eventType: string = item.payload.t
    if (eventType === 'READY') {
      const ready = readyEvent.parse(item.payload.d)
      if (ready.application.id !== identity.applicationID || ready.user.id !== identity.botUserID)
        throw new DiscordAPIError('gateway_identity_mismatch')
    }
    if (eventType === 'MESSAGE_CREATE') {
      const guild = guildEvent.parse(item.payload.d).guild_id
      if (guild) {
        let install
        try {
          install = await raceWithAbort(
            factory.resolveInstallation(guild, identity.botUserID),
            signal,
          )
        } catch (error) {
          if (!isCoreNotFoundError(error)) throw error
        }
        if (install) {
          if (
            install.integration_app_id !== unit.integration_app_id ||
            install.app_configuration_revision !==
              factory.configuration.app.configuration_revision ||
            install.install.provider_tenant_id !== guild ||
            install.install.provider_account_ref !== identity.botUserID
          )
            throw new DiscordAPIError('capture_installation_mismatch')
          signal.throwIfAborted()
          await raceWithAbort(
            context.submitInbound(
              {
                event_id: `discord:${item.position.session.sessionId}:${item.position.session.sequence}`,
                integration_install_id: install.install.id,
                payload: schemas.zChannelEventPayload.parse(item.payload),
              },
              signal,
            ),
            signal,
          )
        }
      }
    }
    // Ordinary SDK/control events require no receipt. A durable relevant receipt
    // must complete first; serial draining never skips a blocked predecessor.
    signal.throwIfAborted()
    checkpoint.captured(item.position, eventType === 'READY')
    if (eventType === 'READY' || eventType === 'RESUMED') announceReady()
  }

  const dispatch = (payload: DiscordDispatch, shardId: number): void => {
    if (signal.aborted) return
    try {
      if (shardId !== shard.shard_id) throw new DiscordAPIError('session_shard_mismatch')
      const bytes = Buffer.byteLength(JSON.stringify(payload))
      if (bytes > maxCaptureBytes) throw new DiscordAPIError('capture_payload_too_large')
      const position = checkpoint.position(payload.s)
      // The minimum charge bounds queue entries/closures as well as payloads.
      // No second count cap: affordable bursts can drain without reconnecting.
      // Behavior filtering occurs after this durable capture, not before it.
      const reservation = reservations.reserve(Math.max(1024, bytes * 8))
      draining = draining
        .then(() => capture({ payload, position }))
        .catch(() => {
          fail(new DiscordAPIError('durable_capture_failed'))
        })
        .finally(() => {
          reservation.release()
        })
    } catch {
      fail(new DiscordAPIError('capture_rejected'))
    }
  }
  try {
    socket = (options.createSocket ?? createDiscordSocket)({
      api,
      botToken: identity.botToken,
      shard,
      signal,
      identify,
      retrieveSessionInfo: checkpoint.retrieve,
      updateSessionInfo: checkpoint.observe,
      onDispatch: dispatch,
      onHeartbeat: (id) => {
        if (id !== shard.shard_id) throw new DiscordAPIError('session_shard_mismatch')
        announceReady()
      },
      onFailure: fail,
      onFatalRuntimeFailure: options.onFatalRuntimeFailure,
      stopTimeoutMs: options.stopTimeoutMs,
    })
    const connected = socket.connect().catch(() => {
      fail(new DiscordAPIError('gateway_connect_failed'))
    })
    await raceWithAbort(connected, signal).catch(() => undefined)
    if (!signal.aborted)
      await new Promise<void>((resolve) => {
        signal.addEventListener(
          'abort',
          () => {
            resolve()
          },
          { once: true },
        )
      })
  } finally {
    lifetime.abort()
    checkpoint.stop() // SDK destroy's null update must not erase the restart prefix.
    try {
      await socket?.stop()
    } finally {
      await raceWithAbort(draining, AbortSignal.timeout(options.stopTimeoutMs)).catch(
        () => undefined,
      )
      reservations.close()
    }
  }
  if (failure) throw failure
}
