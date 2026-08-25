import { describe, expect, it } from 'vitest'

import { zAgentName, zCreateMachineDaemonTokenRequest, zResourceName } from './generated/zod.gen'

describe('generated ResourceName schema', () => {
  it('counts Unicode code points rather than UTF-16 code units', () => {
    expect(zResourceName.safeParse('😀'.repeat(64)).success).toBe(true)
    expect(zResourceName.safeParse('界'.repeat(65)).success).toBe(false)
  })

  it('applies the submitted length policy before normalizing to NFC', () => {
    const result = zResourceName.safeParse('e\u0301'.repeat(32))
    expect(result.success).toBe(true)
    if (result.success) expect(result.data).toBe('é'.repeat(32))
    expect(zResourceName.safeParse('e\u0301'.repeat(33)).success).toBe(false)
    expect(zResourceName.safeParse('\u0344'.repeat(33)).success).toBe(false)
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

describe('generated AgentName schema', () => {
  it('allows an unnamed agent and otherwise applies ResourceName', () => {
    expect(zAgentName.safeParse('').success).toBe(true)
    expect(zAgentName.safeParse('😀'.repeat(64)).success).toBe(true)
    expect(zAgentName.safeParse(' invalid').success).toBe(false)
    expect(zAgentName.parse('Cafe\u0301')).toBe('Café')
  })

  it('does not make other optional resource names empty', () => {
    expect(zCreateMachineDaemonTokenRequest.safeParse({}).success).toBe(true)
    expect(zCreateMachineDaemonTokenRequest.safeParse({ name: '' }).success).toBe(false)
  })
})
