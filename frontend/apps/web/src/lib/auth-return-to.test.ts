/** @vitest-environment happy-dom */

import { describe, expect, it } from 'vitest'

import { safeReturnTo } from '@/lib/auth-return-to'

describe('safeReturnTo', () => {
  it('preserves a local device path', () => {
    expect(safeReturnTo('/device?user_code=ABCDE-F1234')).toBe('/device?user_code=ABCDE-F1234')
  })

  it.each([
    'https://example.com/device',
    `${window.location.origin}//example.com`,
    '//example.com/device',
    '/\\example.com',
    '/safe/..//example.com',
    '/login?return_to=/device',
  ])('rejects unsafe return path %s', (value) => {
    expect(safeReturnTo(value)).toBe('/')
  })
})
