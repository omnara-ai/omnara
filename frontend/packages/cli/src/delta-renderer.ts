import type { ModelOutputDelta } from '@omnara/sdk'

import { ansi, label, type Terminal, terminalColumns } from './terminal.ts'

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
