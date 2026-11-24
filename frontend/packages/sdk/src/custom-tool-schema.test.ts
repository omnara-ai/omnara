import { describe, expect, it } from 'vitest'

import { zAgentConfigDefinitionTool } from './generated/zod.gen'

describe('generated custom tool contract', () => {
  it('keeps JSON Schema keywords the input schema does not list', () => {
    const tool = {
      type: 'custom',
      description: 'Look up an order.',
      input_schema: {
        type: 'object',
        description: 'Order lookup input.',
        properties: { order_id: { type: 'string' } },
        required: ['order_id'],
        additionalProperties: false,
      },
    }
    expect(zAgentConfigDefinitionTool.parse(tool)).toEqual(tool)
  })

  it('keeps a send-only timeout of zero', () => {
    const tool = {
      type: 'custom',
      description: 'Show a notice.',
      input_schema: { type: 'object' },
      timeout_seconds: 0,
    }
    expect(zAgentConfigDefinitionTool.parse(tool)).toEqual(tool)
    expect(zAgentConfigDefinitionTool.safeParse({ ...tool, timeout_seconds: 86401 }).success).toBe(
      false,
    )
  })
})
