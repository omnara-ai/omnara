import { describe, expect, it, vi } from 'vitest'

import type { RuntimeCheckpoint } from '../types'
import { runDiscordUnit } from './runtime'
import { dispatch, runtimeSetup, savedCheckpoint, session } from './runtime-test-support'
import { config, deferred } from './test-support'

describe('Discord durable runtime intake', () => {
  it('holds the prefix through blocked capture and later dispatch, then replays through a fresh runtime', async () => {
    const first = await runtimeSetup({ checkpoint: savedCheckpoint(session) })
    const blocked = deferred()
    const receipts = new Set<string>()
    vi.mocked(first.context.submitInbound).mockImplementation(async (event) => {
      if (event.event_id.endsWith(':2')) await blocked.promise
      receipts.add(event.event_id)
      return { receipt_id: 'receipt', state: 'pending' }
    })
    const running = runDiscordUnit(first.factory, first.unit, first.context, first.options)
    await vi.waitFor(() => {
      expect(first.connect).toHaveBeenCalled()
    })
    const socket = first.socket()
    socket.updateSessionInfo(0, { ...session, sequence: 3 })
    socket.onDispatch(dispatch(2), 0)
    socket.onDispatch(dispatch(3), 0)
    await vi.waitFor(() => {
      expect(first.context.submitInbound).toHaveBeenCalledTimes(1)
    })
    expect(socket.retrieveSessionInfo(0)?.sequence).toBe(1)
    expect(first.context.updateCheckpoint).not.toHaveBeenCalled()
    first.controller.abort()
    await running
    expect(first.stop).toHaveBeenCalledOnce()
    expect(first.budget.usedBytes).toBe(0)
    // A commit whose ACK was lost is safe to replay. Core owns the lease fence
    // and receipt uniqueness; the mock models a commit just before lease loss.
    blocked.resolve()
    await vi.waitFor(() => {
      expect(receipts.size).toBe(1)
    })
    const next = await runtimeSetup({ checkpoint: savedCheckpoint(session) })
    vi.mocked(next.context.submitInbound).mockImplementation((event) => {
      receipts.add(event.event_id)
      return Promise.resolve({ receipt_id: 'receipt', state: 'pending' })
    })
    const restarted = runDiscordUnit(next.factory, next.unit, next.context, next.options)
    await vi.waitFor(() => {
      expect(next.connect).toHaveBeenCalled()
    })
    next.socket().onDispatch(dispatch(2), 0)
    next.socket().onDispatch(dispatch(3), 0)
    await vi.waitFor(() => {
      expect(next.socket().retrieveSessionInfo(0)?.sequence).toBe(3)
    })
    expect(receipts.size).toBe(2)
    expect(next.factory.resolveInstallation).toHaveBeenCalledWith(config.guildID, config.botUserID)
    next.controller.abort()
    await restarted
  })

  it('only checkpoints after durable ACK and preserves that point on intentional shutdown', async () => {
    const f = await runtimeSetup({ checkpoint: savedCheckpoint(session) })
    const running = runDiscordUnit(f.factory, f.unit, f.context, f.options)
    await vi.waitFor(() => {
      expect(f.connect).toHaveBeenCalled()
    })
    f.socket().onDispatch(dispatch(2), 0)
    await vi.waitFor(() => {
      expect(f.context.updateCheckpoint).toHaveBeenCalledTimes(1)
    })
    const saved: RuntimeCheckpoint | undefined = vi.mocked(f.context.updateCheckpoint).mock
      .calls[0]?.[0]
    if (!saved) throw new Error('expected a captured checkpoint')
    expect(saved.checkpoint).toEqual(savedCheckpoint({ ...session, sequence: 2 }))
    expect(vi.mocked(f.context.submitInbound).mock.calls[0]?.[0].payload).toEqual(dispatch(2))
    f.controller.abort()
    await running
    expect(f.socket().retrieveSessionInfo(0)?.sequence).toBe(2)
    expect(f.context.updateCheckpoint).toHaveBeenCalledTimes(1)
  })

  it.each(['capture', 'bytes', 'installation'])(
    'stops safely on %s failure without advancing past uncaptured work',
    async (failure) => {
      const f = await runtimeSetup({ checkpoint: savedCheckpoint(session) })
      if (failure === 'capture')
        vi.mocked(f.context.submitInbound).mockRejectedValue(new Error('unavailable'))
      if (failure === 'installation')
        vi.mocked(f.factory.resolveInstallation).mockRejectedValue(new Error('lease lost'))
      const running = runDiscordUnit(f.factory, f.unit, f.context, f.options)
      const rejected = expect(running).rejects.toThrow()
      await vi.waitFor(() => {
        expect(f.connect).toHaveBeenCalled()
      })
      if (failure === 'bytes') {
        const held = f.budget.reserve(f.budget.limitBytes)
        f.socket().onDispatch(dispatch(2), 0)
        held.release()
      } else f.socket().onDispatch(dispatch(2), 0)
      await rejected
      expect(f.stop).toHaveBeenCalledOnce()
      expect(f.socket().retrieveSessionInfo(0)?.sequence).toBe(1)
      expect(f.budget.usedBytes).toBe(0)
    },
  )

  it('rejects install-scoped or stale app-revision units before socket creation', async () => {
    const f = await runtimeSetup({ lease_app_configuration_revision: 9 })
    await expect(runDiscordUnit(f.factory, f.unit, f.context, f.options)).rejects.toThrow(
      'runtime_scope_mismatch',
    )
    expect(f.createSocket).not.toHaveBeenCalled()
  })

  it('stops on charged payload bytes even when the capture count has ample capacity', async () => {
    const f = await runtimeSetup({ checkpoint: savedCheckpoint(session) })
    const heldBytes = f.budget.limitBytes - 1024 * 1024
    const held = f.budget.reserve(heldBytes)
    const running = runDiscordUnit(f.factory, f.unit, f.context, f.options)
    const rejected = expect(running).rejects.toThrow('capture_rejected')
    try {
      await vi.waitFor(() => {
        expect(f.connect).toHaveBeenCalledOnce()
      })
      const payload = dispatch(2)
      // Under the inbox size limit, but its retained tree/serialization charge
      // cannot fit in the remaining global budget. No 64-item queue is needed.
      if (!payload.d || !('content' in payload.d)) throw new Error('expected message fixture')
      payload.d.content = 'x'.repeat(256 * 1024)
      f.socket().onDispatch(payload, 0)
      await rejected
      expect(f.context.submitInbound).not.toHaveBeenCalled()
      expect(f.context.updateCheckpoint).not.toHaveBeenCalled()
      expect(f.socket().retrieveSessionInfo(0)?.sequence).toBe(1)
      expect(f.budget.usedBytes).toBe(heldBytes)
      expect(f.stop).toHaveBeenCalledOnce()
    } finally {
      held.release()
    }
    expect(f.budget.usedBytes).toBe(0)
  })
})
