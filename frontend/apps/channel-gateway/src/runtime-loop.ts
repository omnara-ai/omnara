import type { ChannelConnectorCapability, ChannelConnectorRuntimeUnit } from '@omnara/sdk'

import type { AppRuntimeRegistry } from './app-registry'
import { abortableDelay, pollJitterMilliseconds } from './async'
import { type CoreClient, isTransientCoreError } from './core-client'
import { errorMessage } from './diagnostics'
import type { GatewayLogger, ProviderWorkReservation, RuntimeCheckpoint } from './types'
import { WorkReservationScope } from './work-budget'

export interface RuntimeLoopOptions {
  capabilities: ChannelConnectorCapability[]
  claimLimit: number
  client: CoreClient
  idlePollMs: number
  leaseMs: number
  logger: GatewayLogger
  owner: string
  random?: () => number
  reserveWorkBytes: (bytes: number) => ProviderWorkReservation
  registry: AppRuntimeRegistry
  stopTimeoutMs: number
}

export class RuntimeLoop {
  private readonly active = new Map<string, Promise<void>>()
  private readonly controllers = new Map<string, AbortController>()

  constructor(private readonly options: RuntimeLoopOptions) {}

  async run(signal: AbortSignal): Promise<void> {
    if (this.options.capabilities.length === 0) return
    let cursor = 0
    let emptyClaims = 0
    while (!signal.aborted) {
      const capacity = this.options.claimLimit - this.active.size
      if (capacity <= 0) {
        await this.pollDelay(signal)
        continue
      }
      const capability = this.options.capabilities[cursor]
      if (!capability) return
      cursor = (cursor + 1) % this.options.capabilities.length
      try {
        const claimStartedAtMs = Date.now()
        const units = await this.options.client.claimRuntimeUnits(
          capability,
          this.options.owner,
          this.options.leaseMs,
          1,
          signal,
        )
        for (const unit of units) {
          const existing = this.controllers.get(unit.id)
          if (existing) {
            existing.abort(new Error('runtime unit lease was reclaimed while still active'))
            await this.release(unit, {
              code: 'duplicate_runtime_claim',
              message: 'runtime unit was reclaimed while its previous work was still active',
            })
            continue
          }
          this.start(unit, claimStartedAtMs + this.options.leaseMs, signal)
        }
        if (units.length === 0) {
          emptyClaims++
          if (emptyClaims >= this.options.capabilities.length) {
            emptyClaims = 0
            await this.pollDelay(signal)
          }
        } else {
          emptyClaims = 0
        }
      } catch (error) {
        if (isAborted(signal)) break
        this.options.logger.error('channel runtime claim loop failed', {
          connector_key: capability.connector_key,
          error: errorMessage(error),
          provider: capability.provider,
        })
        emptyClaims = 0
        await this.pollDelay(signal)
      }
    }
    for (const controller of this.controllers.values())
      controller.abort(new Error('gateway shutdown'))
    await Promise.allSettled(this.active.values())
  }

  private pollDelay(signal: AbortSignal): Promise<boolean> {
    return abortableDelay(
      pollJitterMilliseconds(this.options.idlePollMs, this.options.random),
      signal,
    )
  }

  private start(
    unit: ChannelConnectorRuntimeUnit,
    localLeaseDeadlineMs: number,
    parentSignal: AbortSignal,
  ): void {
    if (this.active.has(unit.id)) return
    const controller = new AbortController()
    const abort = () => {
      controller.abort(parentSignal.reason)
    }
    if (parentSignal.aborted) abort()
    else parentSignal.addEventListener('abort', abort, { once: true })
    this.controllers.set(unit.id, controller)
    const work = this.supervise(unit, localLeaseDeadlineMs, controller.signal)
      .catch((error: unknown) => {
        this.options.logger.error('channel runtime unit failed', {
          error: errorMessage(error),
          runtime_unit_id: unit.id,
        })
      })
      .finally(() => {
        parentSignal.removeEventListener('abort', abort)
        this.controllers.delete(unit.id)
        this.active.delete(unit.id)
      })
    this.active.set(unit.id, work)
  }

  private async supervise(
    unit: ChannelConnectorRuntimeUnit,
    localLeaseDeadlineMs: number,
    signal: AbortSignal,
  ): Promise<void> {
    if (isAborted(signal)) {
      await this.release(unit, {})
      return
    }
    try {
      validateRuntimeClaim(unit)
    } catch (error) {
      await this.release(unit, {
        code: 'runtime_initialization_failed',
        message: errorMessage(error),
      })
      return
    }

    const state: RuntimeLeaseState = { localLeaseDeadlineMs, unit }
    let lastError: Record<string, unknown> = {}
    let leaseFailure: unknown
    let initialized = false
    let handle: Awaited<ReturnType<AppRuntimeRegistry['acquire']>> | undefined
    const operationController = new AbortController()
    const workReservations = new WorkReservationScope(this.options.reserveWorkBytes)
    const abortOperation = () => {
      operationController.abort(signal.reason)
    }
    if (signal.aborted) abortOperation()
    else signal.addEventListener('abort', abortOperation, { once: true })
    const heartbeat = this.heartbeat(state, operationController.signal).then((error) => {
      if (error !== undefined && !operationController.signal.aborted) {
        leaseFailure = error
        operationController.abort(error)
      }
    })
    let settled: Promise<RuntimeOutcome> | undefined
    try {
      handle = await acquireRuntimeHandle(
        this.options.registry,
        unit.integration_app_id,
        unit.lease_app_configuration_revision,
        operationController.signal,
      )
      const installation = unit.integration_install_id
        ? await raceWithAbort(
            handle.getInstallation(
              unit.integration_install_id,
              unit.lease_install_configuration_revision,
            ),
            operationController.signal,
          )
        : undefined
      initialized = true
      if (!handle.runtime.runUnit) {
        lastError = {
          code: 'runtime_not_supported',
          message: 'provider adapter does not support runtime units',
        }
        return
      }
      settled = handle
        .runUnit(state.unit, {
          installation,
          reserveWorkBytes: workReservations.reserve,
          signal: operationController.signal,
          updateCheckpoint: (checkpoint) => {
            state.checkpoint = checkpoint
          },
        })
        .then<RuntimeOutcome, RuntimeOutcome>(
          () => ({ succeeded: true }),
          (error: unknown) => ({ error, succeeded: false }),
        )
      const outcome = await Promise.race([
        settled.then((result) => ({ kind: 'settled' as const, result })),
        waitForAbort(operationController.signal).then(() => ({ kind: 'aborted' as const })),
      ])
      if (outcome.kind === 'settled') {
        if (!outcome.result.succeeded) {
          if (!isAborted(operationController.signal)) throw asError(outcome.result.error)
        } else if (!isAborted(operationController.signal)) {
          lastError = {
            code: 'runtime_stopped',
            message: 'provider runtime stopped unexpectedly',
          }
        }
      }
    } catch (error) {
      if (leaseFailure !== undefined) {
        lastError = { code: 'runtime_lease_lost', message: errorMessage(leaseFailure) }
      } else if (!signal.aborted && !initialized) {
        lastError = { code: 'runtime_initialization_failed', message: errorMessage(error) }
      } else if (!operationController.signal.aborted) {
        lastError = { code: 'runtime_failed', message: errorMessage(error) }
      }
    } finally {
      operationController.abort(new Error('runtime unit supervision ended'))
      signal.removeEventListener('abort', abortOperation)
      await heartbeat
      if (leaseFailure !== undefined) {
        lastError = { code: 'runtime_lease_lost', message: errorMessage(leaseFailure) }
      }
      if (settled && !(await settlesWithin(settled, this.options.stopTimeoutMs))) {
        this.options.logger.error('channel runtime ignored cancellation deadline', {
          runtime_unit_id: unit.id,
          stop_timeout_ms: this.options.stopTimeoutMs,
        })
      }
      workReservations.close()
      await this.release(state.unit, lastError, state.checkpoint)
      await handle?.release()
    }
  }

  private async heartbeat(
    state: RuntimeLeaseState,
    signal: AbortSignal,
  ): Promise<Error | undefined> {
    const intervalMs = Math.max(1, Math.floor(this.options.leaseMs / 3))
    let retryImmediately = false
    while (!signal.aborted) {
      const remainingMs = state.localLeaseDeadlineMs - Date.now()
      if (remainingMs <= 0) return new Error('runtime lease expired before it could be renewed')
      if (!retryImmediately && !(await abortableDelay(Math.min(intervalMs, remainingMs), signal))) {
        return undefined
      }
      retryImmediately = false
      if (Date.now() >= state.localLeaseDeadlineMs) {
        return new Error('runtime lease expired before it could be renewed')
      }
      const checkpoint = state.checkpoint
      const heartbeatStartedAtMs = Date.now()
      try {
        state.unit = await this.options.client.heartbeatRuntimeUnit(
          state.unit,
          this.options.leaseMs,
          checkpoint,
          AbortSignal.any([
            signal,
            AbortSignal.timeout(
              Math.max(1, Math.min(intervalMs, state.localLeaseDeadlineMs - Date.now())),
            ),
          ]),
        )
        state.localLeaseDeadlineMs = heartbeatStartedAtMs + this.options.leaseMs
        if (state.checkpoint === checkpoint) state.checkpoint = undefined
      } catch (error) {
        if (isAborted(signal)) return undefined
        if (isTransientCoreError(error) && Date.now() < state.localLeaseDeadlineMs) {
          this.options.logger.warn('channel runtime heartbeat temporarily failed', {
            error: errorMessage(error),
            runtime_unit_id: state.unit.id,
          })
          retryImmediately = true
          continue
        }
        return error instanceof Error ? error : new Error(String(error))
      }
    }
    return undefined
  }

  private async release(
    unit: ChannelConnectorRuntimeUnit,
    lastError: Record<string, unknown>,
    checkpoint?: RuntimeCheckpoint,
  ): Promise<void> {
    try {
      await this.options.client.releaseRuntimeUnit(
        unit,
        lastError,
        checkpoint,
        AbortSignal.timeout(Math.min(this.options.stopTimeoutMs, 5_000)),
      )
    } catch (error) {
      this.options.logger.warn('release channel runtime unit', {
        error: errorMessage(error),
        runtime_unit_id: unit.id,
      })
    }
  }
}

function validateRuntimeClaim(unit: ChannelConnectorRuntimeUnit): void {
  if (
    unit.lease_spec_revision === undefined ||
    unit.lease_spec_revision !== unit.spec_revision ||
    unit.lease_app_configuration_revision === undefined ||
    (unit.integration_install_id === undefined) !==
      (unit.lease_install_configuration_revision === undefined)
  ) {
    throw new Error('claimed runtime unit is missing a consistent configuration snapshot')
  }
}

type RuntimeOutcome = { succeeded: true } | { error: unknown; succeeded: false }

interface RuntimeLeaseState {
  checkpoint?: RuntimeCheckpoint
  localLeaseDeadlineMs: number
  unit: ChannelConnectorRuntimeUnit
}

function asError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error))
}

function isAborted(signal: AbortSignal): boolean {
  return signal.aborted
}

async function acquireRuntimeHandle(
  registry: AppRuntimeRegistry,
  integrationAppId: string,
  expectedRevision: number | undefined,
  signal: AbortSignal,
): ReturnType<AppRuntimeRegistry['acquire']> {
  if (signal.aborted) throw asError(signal.reason)
  const acquisition = registry.acquire(integrationAppId, expectedRevision)
  try {
    return await raceWithAbort(acquisition, signal)
  } catch (error) {
    void acquisition.then(
      (handle) => handle.release().catch(() => undefined),
      () => undefined,
    )
    throw error
  }
}

function raceWithAbort<T>(work: Promise<T>, signal: AbortSignal): Promise<T> {
  if (signal.aborted) return Promise.reject(asError(signal.reason))
  return new Promise<T>((resolve, reject) => {
    const onAbort = (): void => {
      reject(asError(signal.reason))
    }
    signal.addEventListener('abort', onAbort, { once: true })
    work.then(
      (value) => {
        signal.removeEventListener('abort', onAbort)
        resolve(value)
      },
      (error: unknown) => {
        signal.removeEventListener('abort', onAbort)
        reject(asError(error))
      },
    )
  })
}

function waitForAbort(signal: AbortSignal): Promise<void> {
  if (signal.aborted) return Promise.resolve()
  return new Promise((resolve) => {
    signal.addEventListener(
      'abort',
      () => {
        resolve()
      },
      { once: true },
    )
  })
}

async function settlesWithin(work: Promise<unknown>, timeoutMs: number): Promise<boolean> {
  const controller = new AbortController()
  try {
    return await Promise.race([
      work.then(() => true),
      abortableDelay(timeoutMs, controller.signal).then((elapsed) => !elapsed),
    ])
  } finally {
    controller.abort()
  }
}
