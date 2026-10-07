/** @vitest-environment happy-dom */
import { beforeEach, describe, expect, it } from 'vitest'

import { markStaleChunkReload, staleChunkReloadDue } from '@/lib/stale-chunk'

const now = 1_800_000_000_000

describe('staleChunkReloadDue', () => {
  beforeEach(() => {
    window.sessionStorage.clear()
  })

  it.each([
    'Failed to fetch dynamically imported module: https://app.example/assets/a-1.js',
    'error loading dynamically imported module: https://app.example/assets/a-1.js',
    'Importing a module script failed.',
    'Unable to preload CSS for /assets/a-1.css',
  ])('is due for %j', (message) => {
    expect(staleChunkReloadDue(new Error(message), window, now)).toBe(true)
  })

  it('is not due for other errors', () => {
    expect(staleChunkReloadDue(new Error('boom'), window, now)).toBe(false)
  })

  it('does not read storage for other errors', () => {
    const blocked = {
      get sessionStorage(): Storage {
        throw new Error('storage is blocked')
      },
    }

    expect(staleChunkReloadDue(new Error('boom'), blocked, now)).toBe(false)
  })

  it('is not due again until the cooldown has passed', () => {
    const error = new Error('Importing a module script failed.')
    markStaleChunkReload(window, now)

    expect(staleChunkReloadDue(error, window, now + 59_000)).toBe(false)
    expect(staleChunkReloadDue(error, window, now + 60_000)).toBe(true)
  })
})
