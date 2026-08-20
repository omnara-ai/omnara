import { describe, expect, it } from 'vitest'

import { zDefaultableResourceName, zResourceName } from './generated/zod.gen'

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
    'Acme\u3164Labs',
    'Acme\ufe0fLabs',
    'Acme\u2800Labs',
    'Acme\ud800Labs',
    'Acme\ufffdLabs',
  ])('rejects unsafe presentation characters in %j', (name) => {
    expect(zResourceName.safeParse(name).success).toBe(false)
  })
})

describe('generated DefaultableResourceName schema', () => {
  it('allows the empty default sentinel and otherwise applies ResourceName', () => {
    expect(zDefaultableResourceName.safeParse('').success).toBe(true)
    expect(zDefaultableResourceName.safeParse('😀'.repeat(64)).success).toBe(true)
    expect(zDefaultableResourceName.safeParse(' invalid').success).toBe(false)
  })
})
