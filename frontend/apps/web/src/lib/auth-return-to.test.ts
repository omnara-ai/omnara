/** @vitest-environment happy-dom */

import { describe, expect, it } from 'vitest'

import { safeReturnTo } from '@/lib/auth-return-to'

describe('safeReturnTo', () => {
  it('preserves same-origin device state and rejects external redirects', () => {
    expect(safeReturnTo('/device?user_code=ABCDE-F1234')).toBe('/device?user_code=ABCDE-F1234')
    expect(safeReturnTo('https://example.com/device')).toBe('/')
    expect(safeReturnTo('//example.com/device')).toBe('/')
    expect(safeReturnTo('/login?return_to=/device')).toBe('/')
  })
})
