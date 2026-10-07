/** @vitest-environment happy-dom */
import { beforeEach, describe, expect, it } from 'vitest'

import { claimStaleChunkReload } from '@/lib/stale-chunk'

const now = 1_800_000_000_000
const staleChunk = new Error('Importing a module script failed.')

describe('claimStaleChunkReload', () => {
  beforeEach(() => {
    window.sessionStorage.clear()
  })

  it.each([
    'Failed to fetch dynamically imported module: https://app.example/assets/a-1.js',
    'error loading dynamically imported module: https://app.example/assets/a-1.js',
    'Importing a module script failed.',
    'Unable to preload CSS for /assets/a-1.css',
  ])('claims a reload for %j', (message) => {
    expect(claimStaleChunkReload(new Error(message), window, now)).toBe(true)
  })

  it('does not claim again until the cooldown has passed', () => {
    expect(claimStaleChunkReload(staleChunk, window, now)).toBe(true)
    expect(claimStaleChunkReload(staleChunk, window, now + 59_000)).toBe(false)
    expect(claimStaleChunkReload(staleChunk, window, now + 60_000)).toBe(true)
  })

  it('does not claim for other errors', () => {
    expect(claimStaleChunkReload(new Error('boom'), window, now)).toBe(false)
  })

  it('does not claim when storage cannot be read or written', () => {
    const blocked = {
      get sessionStorage(): Storage {
        throw new Error('storage is blocked')
      },
    }
    const full: Storage = {
      length: 0,
      clear: () => undefined,
      getItem: () => null,
      key: () => null,
      removeItem: () => undefined,
      setItem: () => {
        throw new Error('storage is full')
      },
    }

    expect(claimStaleChunkReload(staleChunk, blocked, now)).toBe(false)
    expect(claimStaleChunkReload(staleChunk, { sessionStorage: full }, now)).toBe(false)
  })
})
