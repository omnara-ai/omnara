import { describe, expect, it } from 'vitest'

import { zDefaultableResourceName, zResourceName, zResourceNameResponse } from './generated/zod.gen'

describe('generated ResourceName schema', () => {
  it('counts Unicode code points rather than UTF-16 code units', () => {
    expect(zResourceName.safeParse('😀'.repeat(64)).success).toBe(true)
    expect(zResourceName.safeParse('界'.repeat(65)).success).toBe(false)
  })

  it.each([
    ' Acme',
    'Acme ',
    'Acme\u00a0Labs',
    'Acme\tLabs',
    'Acme\nLabs',
    'Acme\x00Labs',
    'Acme\u200dLabs',
    'Acme\u202eLabs',
    'Acme\ud800Labs',
  ])('rejects unsafe presentation characters in %j', (name) => {
    expect(zResourceName.safeParse(name).success).toBe(false)
  })
})

describe('generated ResourceNameResponse schema', () => {
  it('accepts grandfathered names returned by the API', () => {
    expect(zResourceNameResponse.safeParse(` ${'a'.repeat(100)} `).success).toBe(true)
  })
})

describe('generated DefaultableResourceName schema', () => {
  it('allows the empty default sentinel and otherwise applies ResourceName', () => {
    expect(zDefaultableResourceName.safeParse('').success).toBe(true)
    expect(zDefaultableResourceName.safeParse('😀'.repeat(64)).success).toBe(true)
    expect(zDefaultableResourceName.safeParse(' invalid').success).toBe(false)
  })
})
