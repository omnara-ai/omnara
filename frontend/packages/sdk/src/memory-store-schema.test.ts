import { describe, expect, it } from 'vitest'

import { zCreateMemoryStore, zMemoryStore, zUpdateMemoryStore } from './generated/zod.gen'

describe('memory store descriptions', () => {
  it.each([
    ['store', zMemoryStore.shape.description],
    ['create', zCreateMemoryStore.shape.description],
    ['update', zUpdateMemoryStore.shape.description],
  ] as const)('%s counts Unicode code points without changing the text', (_, schema) => {
    const description = '😀'.repeat(1024)
    expect(schema.parse(description)).toBe(description)
    expect(schema.safeParse(description + 'x').success).toBe(false)
    expect(schema.parse(' e\u0301\n ')).toBe(' e\u0301\n ')
  })
})
