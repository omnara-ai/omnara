import { describe, expect, it, vi } from 'vitest'

import { createDiscordFactory } from './factory'
import { runtimeSetup, savedCheckpoint, session } from './runtime-test-support'

describe('Discord provider factory lifecycle', () => {
  it('closes active shard work and rejects duplicate owners without touching Chat state', async () => {
    const f = await runtimeSetup({ checkpoint: savedCheckpoint(session) })
    const processReceipt = vi.fn()
    const factory = createDiscordFactory({ ...f.options, processReceipt })
    expect({ connector: factory.connectorKey, provider: factory.provider }).toEqual({
      connector: 'omnara',
      provider: 'discord',
    })
    const runtime = await factory.create(f.factory)
    if (!runtime.runUnit) throw new Error('missing runtime implementation')
    const running = runtime.runUnit(f.unit, f.context)
    await vi.waitFor(() => {
      expect(f.connect).toHaveBeenCalledOnce()
    })
    await expect(runtime.runUnit(f.unit, f.context)).rejects.toThrow(
      'runtime_already_active_or_closed',
    )
    await runtime.close()
    await running
    expect(f.stop).toHaveBeenCalledOnce()
    expect(f.budget.usedBytes).toBe(0)
    expect(runtime.processReceipt).toBe(processReceipt)
    await expect(runtime.runUnit(f.unit, f.context)).rejects.toThrow(
      'runtime_already_active_or_closed',
    )
  })
})
