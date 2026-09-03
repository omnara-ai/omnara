import type { AgentEvent, ListAgentEventsResponse } from '@omnara/sdk'

import type { OutputFormat } from './format.ts'
import { abbreviate } from './output.ts'

interface ContentBlockLike {
  type: string
  text?: string
  name?: string
  value?: unknown
}

export function blockText(block: ContentBlockLike): string {
  switch (block.type) {
    case 'text':
      return block.text ?? ''
    case 'tool_call':
      return `[tool_call ${block.name ?? ''}]`
    case 'structured_data':
      return JSON.stringify(block.value)
    default:
      return `[${block.type}]`
  }
}

const previewWidth = 80

function eventPreview(event: AgentEvent): string {
  switch (event.event_kind) {
    case 'agent_input':
    case 'model_output':
      return abbreviate(event.content_blocks.map(blockText).join(' '), previewWidth)
    case 'tool_result':
      return abbreviate(
        `${event.outcome}: ${event.content_blocks.map(blockText).join(' ')}`,
        previewWidth,
      )
    case 'context_checkpoint':
      return abbreviate(`context summarized: ${event.summary}`, previewWidth)
  }
}

export const formatAgentEventList: OutputFormat<ListAgentEventsResponse> = (response) => ({
  value: {
    data: response.data.map((event) => ({
      sequence: event.sequence,
      event_kind: event.event_kind,
      turn_sequence: event.turn_sequence,
      preview: eventPreview(event),
      created_at: event.created_at,
    })),
    has_more: response.has_more,
    next_after_sequence: response.next_after_sequence,
    ...(response.next_before_sequence == null
      ? {}
      : { next_before_sequence: response.next_before_sequence }),
  },
})

export const summaryWidth = 100

export function toolCallSummary(name: string, input: Record<string, unknown>): string | undefined {
  if (name === 'run_command' && typeof input.command === 'string') {
    return `command: ${abbreviate(input.command, summaryWidth)}`
  }
  const entries = Object.entries(input)
  if (entries.length === 0) return undefined
  return abbreviate(
    entries
      .map(([key, value]) => `${key}: ${typeof value === 'string' ? value : JSON.stringify(value)}`)
      .join(', '),
    summaryWidth,
  )
}
