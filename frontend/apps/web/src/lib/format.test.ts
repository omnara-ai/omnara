import { describe, expect, it } from 'vitest'

import { formatUsd } from '@/lib/format'

describe('formatUsd', () => {
  it('rounds to the nearest cent', () => {
    expect(formatUsd('12.3456')).toBe('$12.35')
    expect(formatUsd('0.016')).toBe('$0.02')
    expect(formatUsd('0')).toBe('$0.00')
  })

  it('shows positive amounts below a cent as <$0.01', () => {
    expect(formatUsd('0.0049')).toBe('<$0.01')
    expect(formatUsd('0.000000000000001')).toBe('<$0.01')
  })
})
