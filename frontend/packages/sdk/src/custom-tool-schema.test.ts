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
})
