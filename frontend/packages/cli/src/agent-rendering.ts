import type { AgentEvent, ListAgentEventsResponse, ModelOutputDelta } from '@omnara/sdk'

import type { OutputFormat } from './format.ts'
import { abbreviate } from './output.ts'
import { ansi, label, type Terminal, terminalColumns } from './terminal.ts'

interface ContentBlockLike {
  type: string
  text?: string
  name?: string
  value?: unknown
}

function blockText(block: ContentBlockLike): string {
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

const summaryWidth = 100

function toolCallSummary(name: string, input: Record<string, unknown>): string | undefined {
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

export interface ToolCallInfo {
  name: string
  summary?: string
}

function printModelText(terminal: Terminal, text: string, streamedText: string | undefined): void {
  if (streamedText === undefined) {
    if (text.trim() !== '') terminal.printBlock(`${label('agent', ansi.green)} ${text}`)
    return
  }
  if (!text.startsWith(streamedText)) {
    if (text.trim() !== '') terminal.printBlock(`${label('agent', ansi.green)} ${text}`)
    return
  }
  const remainder = text.slice(streamedText.length)
  if (remainder.trim() === '') return
  const continuation =
    streamedText.trim() === '' ? `${label('agent', ansi.green)} ${remainder}` : `  ${remainder}`
  terminal.printBlock(continuation)
}

export function printEvent(
  terminal: Terminal,
  event: AgentEvent,
  streamedTextByContext: Map<string, string>,
  toolCalls: Map<string, ToolCallInfo>,
): void {
  switch (event.event_kind) {
    case 'agent_input':
      return
    case 'model_output': {
      const streamedText = streamedTextByContext.get(event.model_call_context_id)
      streamedTextByContext.delete(event.model_call_context_id)
      const fullText = event.content_blocks
        .map((block) => (block.type === 'text' ? block.text : ''))
        .join('')
      printModelText(terminal, fullText, streamedText)
      for (const block of event.content_blocks) {
        if (block.type === 'tool_call') {
          toolCalls.set(block.tool_call_id, {
            name: block.name,
            summary: toolCallSummary(block.name, block.input),
          })
        } else if (block.type === 'error') {
          terminal.printBlock(`${label('error', ansi.red)} ${block.text}`)
        }
      }
      return
    }
    case 'tool_result': {
      const call = toolCalls.get(event.tool_call_id)
      toolCalls.delete(event.tool_call_id)
      const name = call?.name ?? event.tool_call_id
      const output = event.content_blocks
        .filter((block) => block.type === 'text' || block.type === 'structured_data')
        .map(blockText)
        .find((text) => text.trim() !== '')
      const outcomeColor =
        event.outcome === 'succeeded'
          ? ansi.green
          : event.outcome === 'canceled'
            ? ansi.yellow
            : ansi.red
      terminal.printBlock(
        `${label('tool', ansi.magenta)} ${name} ${outcomeColor}(${event.outcome})${ansi.reset}` +
          (call?.summary != null ? `\n  ${ansi.dim}${call.summary}${ansi.reset}` : '') +
          (output != null
            ? `\n  ${ansi.dim}→ ${abbreviate(output, summaryWidth)}${ansi.reset}`
            : ''),
      )
      return
    }
    case 'context_checkpoint':
      terminal.printBlock(`${label('checkpoint', ansi.gray)} context summarized`)
      return
  }
}

export function printHistoryEvent(
  terminal: Terminal,
  event: AgentEvent,
  toolCalls: Map<string, ToolCallInfo>,
): void {
  if (event.event_kind === 'agent_input') {
    if (event.input_kind !== 'content') return
    const text = event.content_blocks
      .map((block) => (block.type === 'text' ? block.text : ''))
      .filter((line) => line.trim() !== '')
      .join('\n')
    if (text !== '') terminal.printBlock(`${label('you', ansi.cyan)} ${text}`)
    return
  }
  printEvent(terminal, event, new Map(), toolCalls)
}

export class DeltaRenderer {
  private contextId = ''
  private buffer = ''
  private startedBlock = false

  constructor(
    private readonly terminal: Terminal,
    private readonly streamedTextByContext: Map<string, string>,
  ) {}

  handle(frame: ModelOutputDelta): void {
    const event = frame.event
    switch (event.kind) {
      case 'text_delta': {
        if (this.contextId !== frame.model_call_context_id) {
          this.flush()
          this.contextId = frame.model_call_context_id
        }
        this.streamedTextByContext.set(
          frame.model_call_context_id,
          (this.streamedTextByContext.get(frame.model_call_context_id) ?? '') + event.delta,
        )
        this.buffer += event.delta
        this.commitReadyLines()
        this.updatePreview()
        return
      }
      case 'block_stop':
      case 'message_stop':
        this.flush()
        return
      case 'error':
        this.flush()
        this.terminal.printBlock(`${label('error', ansi.red)} ${event.error.message}`)
        return
      default:
        return
    }
  }

  complete(contextId: string): void {
    if (this.contextId !== contextId) return
    this.reset()
  }

  reset(): void {
    this.flush()
    this.contextId = ''
  }

  private lineWidthLimit(): number {
    return Math.max(20, terminalColumns() - (this.startedBlock ? 2 : 6) - 1)
  }

  private commitReadyLines(): void {
    for (;;) {
      const limit = this.lineWidthLimit()
      const newline = this.buffer.indexOf('\n')
      if (newline >= 0 && newline <= limit) {
        this.printLine(this.buffer.slice(0, newline))
        this.buffer = this.buffer.slice(newline + 1)
        continue
      }
      if (this.buffer.length > limit) {
        const head = this.buffer.slice(0, limit)
        const space = head.lastIndexOf(' ')
        const cut = space > limit / 2 ? space : limit
        this.printLine(this.buffer.slice(0, cut))
        this.buffer = this.buffer.slice(cut).replace(/^ /, '')
        continue
      }
      return
    }
  }

  private updatePreview(): void {
    if (this.buffer === '' && !this.startedBlock) {
      this.terminal.setPreview(undefined)
      return
    }
    const prefix = this.startedBlock ? '  ' : `${label('agent', ansi.green)} `
    this.terminal.setPreview(`${prefix}${this.buffer}`)
  }

  private flush(): void {
    if (this.buffer.trim() !== '') this.printLine(this.buffer)
    this.buffer = ''
    this.startedBlock = false
    this.terminal.setPreview(undefined)
  }

  private printLine(line: string): void {
    if (line.trim() === '' && !this.startedBlock) return
    if (!this.startedBlock) {
      this.terminal.blankLine()
      this.terminal.print(`${label('agent', ansi.green)} ${line}`)
      this.startedBlock = true
      return
    }
    this.terminal.print(`  ${line}`)
  }
}
