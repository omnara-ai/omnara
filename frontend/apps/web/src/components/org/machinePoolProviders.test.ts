import { describe, expect, it } from 'vitest'

import { machinePoolScopeValue } from './machinePoolProviders'

describe('machine pool scope display', () => {
  it.each([
    [undefined, 'omnara'],
    ['', 'omnara'],
    ['  ', 'omnara'],
    ['agents', 'agents'],
    [' agents ', 'agents'],
  ])('shows the effective Modal app for %j', (app, expected) => {
    expect(machinePoolScopeValue('modal', app === undefined ? {} : { app })).toBe(expected)
  })

  it('does not apply the Modal default to a Blaxel workspace', () => {
    expect(machinePoolScopeValue('blaxel', { workspace: '' })).toBe('')
  })
})
