import { describe, expect, it } from 'vitest'
import * as z from 'zod'

import { zAgentEvent, zCreateAgentResponse, zListTurnEventsResponse } from './generated/zod.gen'
import type { JsonBody } from './json-body'
import { relaxedResponseValidator, relaxedSchema } from './validate-response'

function outcome(schema: z.ZodType, data: JsonBody): 'accepted' | 'rejected' {
  return relaxedSchema(schema).safeParse(data).success ? 'accepted' : 'rejected'
}

async function validatorOutcome(
  schema: z.ZodType,
  data: JsonBody,
): Promise<'accepted' | 'rejected'> {
  try {
    await relaxedResponseValidator(schema)(data)
    return 'accepted'
  } catch {
    return 'rejected'
  }
}

describe('relaxedSchema', () => {
  it('accepts data that matches the schema', () => {
    expect(outcome(z.object({ a: z.string() }), { a: 'x' })).toBe('accepted')
  })

  it('accepts an unknown value for a plain enum', () => {
    expect(outcome(z.enum(['x', 'y']), 'brand-new')).toBe('accepted')
  })

  it('accepts an unknown value for a union of enums', () => {
    const schema = z.union([z.enum(['p', 'q']), z.enum(['r', 's'])])
    expect(outcome(schema, 'brand-new')).toBe('accepted')
  })

  it('accepts an unknown discriminator in a discriminated union', () => {
    expect(outcome(zAgentEvent, { event_kind: 'brand-new' })).toBe('accepted')
  })

  it('accepts an unknown enum value nested inside a matched union arm', () => {
    const schema = z.union([
      z.object({ kind: z.literal('a'), state: z.enum(['on', 'off']) }),
      z.object({ kind: z.literal('b') }),
    ])
    expect(outcome(schema, { kind: 'a', state: 'brand-new' })).toBe('accepted')
  })

  it('accepts an unknown tag in a plain union of tagged objects', () => {
    const schema = z.union([
      z.object({ kind: z.literal('a'), x: z.number() }),
      z.object({ kind: z.literal('b'), y: z.number() }),
    ])
    expect(outcome(schema, { kind: 'brand-new' })).toBe('accepted')
  })

  it('rejects a literal mismatch outside a union', () => {
    const schema = z.object({ is_opening_event: z.literal(false) })
    expect(outcome(schema, { is_opening_event: true })).toBe('rejected')
  })

  it('rejects a literal mismatch inside a matched union arm', () => {
    const schema = z.union([
      z.object({ kind: z.literal('a'), flag: z.literal(false) }),
      z.object({ kind: z.literal('b'), y: z.number() }),
    ])
    expect(outcome(schema, { kind: 'a', flag: true })).toBe('rejected')
  })

  it('rejects wrong-shaped data against a real union response', () => {
    expect(outcome(zCreateAgentResponse, 'not an object')).toBe('rejected')
  })

  it('rejects a malformed event of a known kind in a real event payload', () => {
    const event = {
      event_kind: 'model_output',
      id: 'not-an-event-id',
    }
    expect(outcome(zListTurnEventsResponse, { data: [event] })).toBe('rejected')
  })
})

describe('relaxedResponseValidator', () => {
  it('accepts an unknown enum value and rejects a contract mismatch', async () => {
    await expect(validatorOutcome(z.enum(['x', 'y']), 'brand-new')).resolves.toBe('accepted')
    await expect(validatorOutcome(zCreateAgentResponse, 'not an object')).resolves.toBe('rejected')
  })
})
