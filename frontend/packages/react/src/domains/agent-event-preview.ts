import type { AgentEvent } from '@omnara/sdk'

interface ContentBlockLike {
  type: string
  text?: string
  name?: string
  value?: unknown
}

function truncate(value: string, width: number): string {
  if (value.length <= width) return value
  let end = Math.max(0, width - 1)
  const lastUnit = value.charCodeAt(end - 1)
  if (end > 0 && lastUnit >= 0xd800 && lastUnit <= 0xdbff) end -= 1
  return `${value.slice(0, end)}…`
}

export function abbreviate(text: string, max: number): string {
  return truncate(text.replaceAll(/\s+/g, ' ').trim(), max)
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

export const agentEventPreviewWidth = 80

export function agentEventPreview(event: AgentEvent, width = agentEventPreviewWidth): string {
  switch (event.event_kind) {
    case 'agent_input':
    case 'model_output':
      return abbreviate(event.content_blocks.map(blockText).join(' '), width)
    case 'tool_result':
      return abbreviate(`${event.outcome}: ${event.content_blocks.map(blockText).join(' ')}`, width)
    case 'context_checkpoint':
      return abbreviate(`context summarized: ${event.summary}`, width)
  }
}
