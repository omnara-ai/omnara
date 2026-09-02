import { describe, expect, it, vi } from 'vitest'

describe('AbortSignal.any polyfill', () => {
  it('installs a composite signal where the platform lacks one', async () => {
    const statics: { any?: (signals: AbortSignal[]) => AbortSignal } = AbortSignal
    const native = statics.any
    delete statics.any
    vi.resetModules()
    try {
      await import('./polyfills')
      // A fresh binding: TypeScript narrowed `statics.any` to undefined after the delete above.
      const installed: { any?: (signals: AbortSignal[]) => AbortSignal } = AbortSignal
      const any = installed.any
      if (any == null) throw new Error('polyfill did not install')
      expect(any).not.toBe(native)

      const first = new AbortController()
      const second = new AbortController()
      const combined = any([first.signal, second.signal])
      expect(combined.aborted).toBe(false)
      second.abort('stop')
      expect(combined.aborted).toBe(true)
      expect(combined.reason).toBe('stop')

      const early = new AbortController()
      early.abort('early')
      expect(any([first.signal, early.signal]).reason).toBe('early')
    } finally {
      if (native == null) delete statics.any
      else statics.any = native
    }
  })
})
