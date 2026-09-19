import { zEventWebhookPayload } from '@omnara/sdk/zod'
import { describe, expect, it } from 'vitest'

import docs from '../../../../../docs/events/webhooks.mdx?raw'

const examples = [...docs.matchAll(/```json\n([\s\S]*?)\n```/g)].map((match) =>
  zEventWebhookPayload.parse(JSON.parse(match[1] ?? '')),
)

describe('event webhook payloads', () => {
  it('validates the documented example for each supported event', () => {
    expect(examples.map((example) => example.event)).toEqual([
      'agent_input',
      'model_output',
      'tool_result',
      'context_checkpoint',
      'tool_call_update',
    ])
  })

  it.each(examples)('rejects mismatched data for $event', (example) => {
    for (const other of examples) {
      if (other.event === example.event) continue
      expect(zEventWebhookPayload.safeParse({ ...example, data: other.data }).success).toBe(false)
    }
    expect(zEventWebhookPayload.safeParse({ ...example, event: 'unknown' }).success).toBe(false)
  })
})
