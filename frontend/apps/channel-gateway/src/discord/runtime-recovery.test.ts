import { ApiError, type ChannelConnectorRuntimeUnit } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import { runDiscordUnit } from './runtime'
import { dispatch, ready, runtimeSetup, savedCheckpoint, session } from './runtime-test-support'
import { config, deferred } from './test-support'

describe('Discord runtime session recovery', () => {
  it.each([404, 503])('only consumes an unmapped guild; lookup status %s', async (status) => {
    const f = await runtimeSetup({ checkpoint: savedCheckpoint(session) })
    f.factory.resolveInstallation.mockRejectedValueOnce(new ApiError(status, 'lookup failed'))
    const running = runDiscordUnit(f.factory, f.unit, f.context, f.options)
    const rejected =
      status === 404 ? undefined : expect(running).rejects.toThrow('durable_capture_failed')
    await vi.waitFor(() => {
      expect(f.connect).toHaveBeenCalledOnce()
    })
    f.socket().onDispatch(dispatch(2), 0)
    f.socket().onDispatch(dispatch(3), 0)
    if (status === 404) {
      await vi.waitFor(() => {
        expect(f.socket().retrieveSessionInfo(0)?.sequence).toBe(3)
      })
      expect(f.context.submitInbound).toHaveBeenCalledOnce()
      expect(f.context.submitInbound.mock.calls[0]?.[0].event_id).toBe(
        `discord:${session.sessionId}:3`,
      )
      expect(f.context.updateCheckpoint.mock.calls.map(([saved]) => saved.checkpoint)).toEqual(
        [2, 3].map((sequence) => savedCheckpoint({ ...session, sequence })),
      )
      f.controller.abort()
      await running
    } else {
      await rejected
      expect(f.socket().retrieveSessionInfo(0)?.sequence).toBe(1)
      expect(f.context.submitInbound).not.toHaveBeenCalled()
      expect(f.context.updateCheckpoint).not.toHaveBeenCalled()
      expect(f.factory.resolveInstallation).toHaveBeenCalledOnce()
    }
    expect(f.stop).toHaveBeenCalledOnce()
    expect(f.budget.usedBytes).toBe(0)
  })

  it('aborts an in-flight receipt submission on internal capture overload', async () => {
    const f = await runtimeSetup({ checkpoint: savedCheckpoint(session) })
    const heldBytes = f.budget.limitBytes - 64 * 1024
    const held = f.budget.reserve(heldBytes)
    const receipts = new Set<string>()
    let submittedSignal: AbortSignal | undefined
    const interrupted = vi.fn()
    f.context.submitInbound.mockImplementation((event, signal) => {
      if (!signal) throw new Error('capture signal required')
      receipts.add(event.event_id) // Model an accepted receipt whose ACK is still blocked.
      submittedSignal = signal
      return new Promise((_resolve, reject) => {
        signal.addEventListener(
          'abort',
          () => {
            interrupted()
            reject(signal.reason instanceof Error ? signal.reason : new Error('capture aborted'))
          },
          { once: true },
        )
      })
    })
    const running = runDiscordUnit(f.factory, f.unit, f.context, f.options)
    const rejected = expect(running).rejects.toThrow('capture_rejected')
    await vi.waitFor(() => {
      expect(f.connect).toHaveBeenCalledOnce()
    })
    f.socket().onDispatch(dispatch(2), 0)
    await vi.waitFor(() => {
      expect(f.context.submitInbound).toHaveBeenCalledOnce()
    })
    expect(submittedSignal?.aborted).toBe(false)
    for (let sequence = 3; sequence <= 66; sequence++) {
      f.socket().onDispatch(dispatch(sequence), 0)
      expect(f.budget.usedBytes).toBeLessThanOrEqual(f.budget.limitBytes)
    }
    await rejected
    expect(interrupted).toHaveBeenCalledOnce()
    expect(submittedSignal?.aborted).toBe(true)
    expect(f.controller.signal.aborted).toBe(false)
    expect(f.context.updateCheckpoint).not.toHaveBeenCalled()
    expect(f.socket().retrieveSessionInfo(0)?.sequence).toBe(1)
    expect(f.budget.usedBytes).toBe(heldBytes)
    held.release()
    expect(f.budget.usedBytes).toBe(0)

    // The fresh owner resumes the saved prefix. The first committed receipt is
    // replayed too; core's stable event identity absorbs that duplicate.
    const next = await runtimeSetup({ checkpoint: savedCheckpoint(session) })
    next.context.submitInbound.mockImplementation((event) => {
      receipts.add(event.event_id)
      return Promise.resolve({ receipt_id: 'receipt', state: 'pending' })
    })
    const restarted = runDiscordUnit(next.factory, next.unit, next.context, next.options)
    await vi.waitFor(() => {
      expect(next.connect).toHaveBeenCalledOnce()
    })
    for (let sequence = 2; sequence <= 66; sequence++)
      next.socket().onDispatch(dispatch(sequence), 0)
    await vi.waitFor(() => {
      expect(next.socket().retrieveSessionInfo(0)?.sequence).toBe(66)
    })
    expect(next.context.submitInbound).toHaveBeenCalledTimes(65)
    expect(receipts.size).toBe(65)
    next.controller.abort()
    await restarted
    expect(next.budget.usedBytes).toBe(0)
  })

  it('drains more than 64 small received messages in order under the shared byte budget', async () => {
    const f = await runtimeSetup({ checkpoint: savedCheckpoint(session) })
    const blocked = deferred()
    f.context.submitInbound.mockImplementationOnce(async () => {
      await blocked.promise
      return { receipt_id: 'first', state: 'pending' }
    })
    const running = runDiscordUnit(f.factory, f.unit, f.context, f.options)
    await vi.waitFor(() => {
      expect(f.connect).toHaveBeenCalledOnce()
    })
    const sequences = Array.from({ length: 128 }, (_, index) => index + 2)
    for (const sequence of sequences) f.socket().onDispatch(dispatch(sequence), 0)
    await vi.waitFor(() => {
      expect(f.context.submitInbound).toHaveBeenCalledOnce()
    })
    expect(f.budget.usedBytes).toBeGreaterThanOrEqual(128 * 1024)
    expect(f.budget.usedBytes).toBeLessThan(f.budget.limitBytes)
    expect(f.stop).not.toHaveBeenCalled()
    expect(f.socket().retrieveSessionInfo(0)?.sequence).toBe(1)
    blocked.resolve()
    await vi.waitFor(() => {
      expect(f.socket().retrieveSessionInfo(0)?.sequence).toBe(129)
    })
    expect(f.context.submitInbound.mock.calls.map(([event]) => event.event_id)).toEqual(
      sequences.map((sequence) => `discord:${session.sessionId}:${sequence}`),
    )
    expect(f.stop).not.toHaveBeenCalled()
    f.controller.abort()
    await running
    expect(f.budget.usedBytes).toBe(0)
  })

  it('keeps the durable prefix when reconnect replays while an earlier capture is blocked', async () => {
    const f = await runtimeSetup({ checkpoint: savedCheckpoint(session) })
    const blocked = deferred()
    const receipts = new Set<string>()
    f.context.submitInbound.mockImplementation(async (event) => {
      if (event.event_id.endsWith(':2')) await blocked.promise
      receipts.add(event.event_id)
      return { receipt_id: 'receipt', state: 'pending' }
    })
    const running = runDiscordUnit(f.factory, f.unit, f.context, f.options)
    await vi.waitFor(() => {
      expect(f.connect).toHaveBeenCalledOnce()
    })
    const socket = f.socket()
    socket.updateSessionInfo(0, { ...session, sequence: 3 })
    socket.onDispatch(dispatch(2), 0)
    socket.onDispatch(dispatch(3), 0)
    await vi.waitFor(() => {
      expect(f.context.submitInbound).toHaveBeenCalledOnce()
    })
    // The public SDK reconnect hook retrieves the captured point, then replays
    // from it. This tests the production queue against that callback sequence;
    // the separate real-socket experiment proves the pinned SDK's hook order.
    expect(socket.retrieveSessionInfo(0)?.sequence).toBe(1)
    socket.updateSessionInfo(0, { ...session, sequence: 4 })
    for (const sequence of [2, 3, 4]) socket.onDispatch(dispatch(sequence), 0)
    expect(f.context.updateCheckpoint).not.toHaveBeenCalled()
    blocked.resolve()
    await vi.waitFor(() => {
      expect(socket.retrieveSessionInfo(0)?.sequence).toBe(4)
    })
    expect(receipts).toEqual(new Set([2, 3, 4].map((n) => `discord:${session.sessionId}:${n}`)))
    expect(f.context.submitInbound).toHaveBeenCalledTimes(5)
    expect(f.context.updateCheckpoint.mock.calls.map(([saved]) => saved.checkpoint)).toEqual(
      [2, 3, 4].map((sequence) => savedCheckpoint({ ...session, sequence })),
    )
    f.controller.abort()
    await running
    expect(f.budget.usedBytes).toBe(0)
  })

  it('saves queued old-session messages without advancing the replacement session checkpoint', async () => {
    const f = await runtimeSetup({ checkpoint: savedCheckpoint(session) })
    const blocked = deferred()
    f.context.submitInbound.mockImplementationOnce(async () => {
      await blocked.promise
      return { receipt_id: 'old-receipt', state: 'pending' }
    })
    const running = runDiscordUnit(f.factory, f.unit, f.context, f.options)
    await vi.waitFor(() => {
      expect(f.connect).toHaveBeenCalledOnce()
    })
    const socket = f.socket()
    socket.onDispatch(dispatch(2), 0)
    socket.onDispatch(dispatch(3), 0)
    await vi.waitFor(() => {
      expect(f.context.submitInbound).toHaveBeenCalledOnce()
    })
    socket.updateSessionInfo(0, null)
    const next = { ...session, sessionId: 'next-session' }
    socket.updateSessionInfo(0, next)
    socket.onDispatch(ready(1, config.botUserID, next.sessionId), 0)
    socket.onDispatch(dispatch(2), 0)
    expect(socket.retrieveSessionInfo(0)).toBeNull()
    blocked.resolve()
    await vi.waitFor(() => {
      expect(socket.retrieveSessionInfo(0)).toEqual({ ...next, sequence: 2 })
    })
    expect(f.context.submitInbound.mock.calls.map(([event]) => event.event_id)).toEqual([
      `discord:${session.sessionId}:2`,
      `discord:${session.sessionId}:3`,
      'discord:next-session:2',
    ])
    expect(f.context.updateCheckpoint.mock.calls.map(([saved]) => saved.checkpoint)).toEqual([
      savedCheckpoint(null),
      savedCheckpoint(next),
      savedCheckpoint({ ...next, sequence: 2 }),
    ])
    f.controller.abort()
    await running
    expect(f.budget.usedBytes).toBe(0)
  })

  it.each([true, false])(
    'validates READY identity before the first saved prefix (valid=%s)',
    async (valid) => {
      const f = await runtimeSetup({ checkpoint: {} })
      const running = runDiscordUnit(f.factory, f.unit, f.context, f.options)
      const rejected = valid ? undefined : expect(running).rejects.toThrow('durable_capture_failed')
      await vi.waitFor(() => {
        expect(f.connect).toHaveBeenCalledOnce()
      })
      expect(f.socket().retrieveSessionInfo(0)).toBeNull()
      f.socket().updateSessionInfo(0, session)
      f.socket().onDispatch(ready(1, valid ? config.botUserID : config.guildID), 0)
      if (valid) {
        await vi.waitFor(() => {
          expect(f.socket().retrieveSessionInfo(0)).toEqual(session)
        })
        f.controller.abort()
        await running
      } else {
        await rejected
        expect(f.context.updateCheckpoint).not.toHaveBeenCalled()
      }
      expect(f.context.submitInbound).not.toHaveBeenCalled()
      expect(f.budget.usedBytes).toBe(0)
    },
  )

  it.each<Partial<ChannelConnectorRuntimeUnit>>([
    { unit_key: 'discord_gateway:1' },
    { runtime_kind: 'another_runtime' },
    { lease_spec_revision: 9 },
    { configuration: { shard_id: 1, shard_count: 1 } },
    { configuration: { shard_id: 0, shard_count: 1, extra: true } },
  ])('rejects invalid unit scope/configuration before socket creation: %j', async (override) => {
    const f = await runtimeSetup(override)
    await expect(runDiscordUnit(f.factory, f.unit, f.context, f.options)).rejects.toThrow()
    expect(f.createSocket).not.toHaveBeenCalled()
    expect(f.context.submitInbound).not.toHaveBeenCalled()
  })
})
