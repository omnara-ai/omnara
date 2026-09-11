import { abbreviate, agentEventPreview } from '@omnara/react'
import type { ListAgentEventsResponse } from '@omnara/sdk'
import * as z from 'zod'

import type { OutputFormat } from './format.ts'

export const formatAgentEventList: OutputFormat<ListAgentEventsResponse> = (response) => {
  const page = {
    data: response.data.map((event) => ({
      sequence: event.sequence,
      event_kind: event.event_kind,
      turn_sequence: event.turn_sequence,
      preview: agentEventPreview(event),
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
