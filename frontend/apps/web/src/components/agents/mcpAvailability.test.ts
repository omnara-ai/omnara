import { describe, expect, it } from 'vitest'

import {
  type McpAvailability,
  mcpToolAvailability,
  mcpToolAvailabilityPatch,
} from '@/components/agents/mcpAvailability'

const availabilities: McpAvailability[] = ['enabled', 'deferred', 'disabled']

describe('mcpToolAvailability', () => {
  it('shows the server default for tools without an override', () => {
    for (const server of availabilities) {
      expect(mcpToolAvailability(undefined, server)).toBe(server)
      expect(mcpToolAvailability({ enabled: null, deferred: null }, server)).toBe(server)
    }
  })

  it('shows explicit overrides', () => {
    expect(mcpToolAvailability({ enabled: false }, 'enabled')).toBe('disabled')
    expect(mcpToolAvailability({ enabled: true, deferred: false }, 'deferred')).toBe('enabled')
    expect(mcpToolAvailability({ enabled: true, deferred: true }, 'disabled')).toBe('deferred')
    expect(mcpToolAvailability({ enabled: true }, 'disabled')).toBe('enabled')
    expect(mcpToolAvailability({ enabled: true }, 'deferred')).toBe('deferred')
  })
})

describe('mcpToolAvailabilityPatch', () => {
  it('clears the override when the choice matches the server default', () => {
    for (const server of availabilities) {
      expect(mcpToolAvailabilityPatch(server, server)).toEqual({ enabled: null, deferred: null })
    }
  })

  it('round-trips every choice against every server default', () => {
    for (const server of availabilities) {
      for (const choice of availabilities) {
        expect(mcpToolAvailability(mcpToolAvailabilityPatch(choice, server), server)).toBe(choice)
      }
    }
  })
})
