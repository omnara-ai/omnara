import { agentEventsToMessages } from '@omnara/react'
import type { AgentEvent } from '@omnara/sdk'
import { expect, it } from 'vitest'

import { toolOutput, type ToolPart } from './chat-ui.tsx'

const base = {
  org_id: 'org',
  project_id: 'project',
  agent_id: 'agent',
  agent_profile_id: null,
  turn_id: 'turn',
  turn_sequence: 2,
  is_opening_event: false as const,
  created_at: '2026-07-14T00:00:00Z',
}

function toolPart(outcome: 'succeeded' | 'denied'): ToolPart {
  const events: AgentEvent[] = [
    {
      ...base,
      id: 'call-event',
      sequence: 11,
      event_kind: 'model_output',
      model_call_context_id: 'mcc',
      stop_reason: 'tool_use',
      content_blocks: [
        {
          type: 'tool_call',
          tool_call_id: 'call',
          tool_type: 'built_in',
          name: 'shell',
          input: { command: 'pwd' },
        },
      ],
    },
    {
      ...base,
      id: 'result-event',
      sequence: 12,
      event_kind: 'tool_result',
      tool_call_id: 'call',
      outcome,
      content_blocks: [{ type: 'text', text: '/workspace' }],
    },
  ]
  const part = agentEventsToMessages(events)
    .flatMap((message) => message.parts)
    .find((candidate) => candidate.type === 'dynamic-tool')
  if (part?.type !== 'dynamic-tool') throw new Error('Missing tool part')
  return part
}

it.each(['succeeded', 'denied'] as const)(
  'shows the result of a %s tool call from agent events',
  (outcome) => {
    expect(toolOutput(toolPart(outcome))).toEqual({ outcome, text: '/workspace' })
  },
)
