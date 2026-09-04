import type { AgentEvent, ListAgentEventsResponse } from '@omnara/sdk'
import * as z from 'zod'

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

export const formatAgentEventList: OutputFormat<ListAgentEventsResponse> = (response) => {
  const page = {
    data: response.data.map((event) => ({
      sequence: event.sequence,
      event_kind: event.event_kind,
      turn_sequence: event.turn_sequence,
      preview: eventPreview(event),
      created_at: event.created_at,
    })),
    has_more: response.has_more,
    next_after_sequence: response.next_after_sequence,
  }
  const { next_before_sequence } = response
  return { value: next_before_sequence == null ? page : { ...page, next_before_sequence } }
}

export const summaryWidth = 100

const zJsonValue = z.json()
type JsonValue = z.output<typeof zJsonValue>
const zRunCommandInput = z.object({ command: z.string() })

function summaryValue(value: JsonValue): string {
  const text = z.string().safeParse(value)
  return text.success ? text.data : JSON.stringify(value)
}

export function toolCallSummary(
  name: string,
  input: Record<string, JsonValue>,
): string | undefined {
  const runCommand = name === 'run_command' ? zRunCommandInput.safeParse(input) : undefined
  if (runCommand?.success) return `command: ${abbreviate(runCommand.data.command, summaryWidth)}`
  const entries = Object.entries(input)
  if (entries.length === 0) return undefined
  return abbreviate(
    entries.map(([key, value]) => `${key}: ${summaryValue(value)}`).join(', '),
    summaryWidth,
  )
}
