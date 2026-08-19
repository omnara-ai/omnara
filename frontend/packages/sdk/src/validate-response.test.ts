import { describe, expect, it } from 'vitest'
import * as z from 'zod'

import { zAgentEvent, zCreateAgentResponse, zListTurnEventsResponse } from './generated/zod.gen'
import { validateResponse } from './validate-response'

async function outcome(schema: z.ZodType, data: unknown): Promise<'accepted' | 'rejected'> {
  try {
    await validateResponse(schema, data)
    return 'accepted'
  } catch {
    return 'rejected'
  }
}

describe('validateResponse', () => {
  it('accepts data that matches the schema', async () => {
    await expect(outcome(z.object({ a: z.string() }), { a: 'x' })).resolves.toBe('accepted')
  })

  it('accepts an unknown value for a plain enum', async () => {
    await expect(outcome(z.enum(['x', 'y']), 'brand-new')).resolves.toBe('accepted')
  })

  it('accepts an unknown value for a union of enums', async () => {
    const schema = z.union([z.enum(['p', 'q']), z.enum(['r', 's'])])
    await expect(outcome(schema, 'brand-new')).resolves.toBe('accepted')
  })

  it('accepts an unknown discriminator in a discriminated union', async () => {
    await expect(outcome(zAgentEvent, { event_kind: 'brand-new' })).resolves.toBe('accepted')
  })

  it('accepts an unknown enum value nested inside a matched union arm', async () => {
    const schema = z.union([
      z.object({ kind: z.literal('a'), state: z.enum(['on', 'off']) }),
      z.object({ kind: z.literal('b') }),
    ])
    await expect(outcome(schema, { kind: 'a', state: 'brand-new' })).resolves.toBe('accepted')
  })

  it('accepts an unknown tag in a plain union of tagged objects', async () => {
    const schema = z.union([
      z.object({ kind: z.literal('a'), x: z.number() }),
      z.object({ kind: z.literal('b'), y: z.number() }),
    ])
    await expect(outcome(schema, { kind: 'brand-new' })).resolves.toBe('accepted')
  })

  it('rejects a literal mismatch outside a union', async () => {
    const schema = z.object({ is_opening_event: z.literal(false) })
    await expect(outcome(schema, { is_opening_event: true })).resolves.toBe('rejected')
  })

  it('rejects a literal mismatch inside a matched union arm', async () => {
    const schema = z.union([
      z.object({ kind: z.literal('a'), flag: z.literal(false) }),
      z.object({ kind: z.literal('b'), y: z.number() }),
    ])
    await expect(outcome(schema, { kind: 'a', flag: true })).resolves.toBe('rejected')
  })

  it('rejects wrong-shaped data against a real union response', async () => {
    await expect(outcome(zCreateAgentResponse, 'not an object')).resolves.toBe('rejected')
  })

  it('rejects a malformed event of a known kind in a real event payload', async () => {
    const event = {
      event_kind: 'model_output',
      id: 'not-an-event-id',
    }
    await expect(outcome(zListTurnEventsResponse, { data: [event] })).resolves.toBe('rejected')
  })
})
