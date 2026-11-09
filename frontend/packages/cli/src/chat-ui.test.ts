import { agentEventsToMessages } from '@omnara/react'
import type { AgentEvent, AgentInteraction } from '@omnara/sdk'
import { renderToString } from 'ink'
import { createElement } from 'react'
import { expect, it } from 'vitest'

import { InteractionPrompt } from './chat-prompts.tsx'
import { toolOutput, type ToolPart } from './chat-ui.tsx'

const base = {
  org_id: 'org',
  project_id: 'project',
  agent_id: 'agent',
  agent_profile_id: null,
  parent_agent_id: null,
  root_agent_id: 'agent',
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

it.each([
  { ownerID: 'agent', label: null },
  { ownerID: 'child', label: 'from subagent Worker' },
])('labels interactions by owning agent $ownerID, not its name', ({ ownerID, label }) => {
  const interaction: AgentInteraction = {
    id: 'interaction',
    org_id: 'org',
    project_id: 'project',
    agent_id: ownerID,
    agent_name: 'Worker',
    subagent_key: 'worker',
    tool_call_id: 'call',
    interaction_kind: 'question',
    state: 'open',
    request: {
      title: 'Which option?',
      questions: [{ prompt: 'Choose one', options: [{ label: 'First' }] }],
    },
    created_at: base.created_at,
  }
  const output = renderToString(
    createElement(InteractionPrompt, {
      interaction,
      agentID: 'agent',
      onAnswer: () => undefined,
    }),
  )
  if (label == null) expect(output).not.toContain('from subagent')
  else expect(output).toContain(label)
})
