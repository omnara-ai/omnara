import type { ModelOutputDelta } from '@omnara/sdk'

interface DeltaTerminal {
  printBlock(text: string): void
  setPreview(text: string | undefined): void
}

export interface AgentRenderState {
  text: string
  detail?: string
}

export interface ToolCallInfo {
  name: string
  summary?: string
  provisional?: boolean
}

export class DeltaRenderer {
  private contextId = ''
  private buffer = ''

  constructor(
    private readonly terminal: DeltaTerminal,
    private readonly agentLabel: string,
    private readonly errorLabel: string,
  ) {}

  handle(frame: ModelOutputDelta): void {
    const event = frame.event
    switch (event.kind) {
      case 'text_delta': {
        if (this.contextId !== frame.model_call_context_id) {
          this.discard()
          this.contextId = frame.model_call_context_id
        }
        const sourceLimit = this.lineWidthLimit() * 2
        this.buffer = (this.buffer + event.delta).slice(-sourceLimit)
        this.updatePreview()
        return
      }
      case 'error':
        this.discard()
        this.terminal.printBlock(`${this.errorLabel} ${event.error.message}`)
        return
      default:
        return
    }
  }

  discard(): void {
    this.buffer = ''
    this.contextId = ''
    this.terminal.setPreview(undefined)
  }

  complete(contextId: string): void {
    if (this.contextId === contextId) this.discard()
  }

  private lineWidthLimit(): number {
    const columns = process.stdout.columns ?? 80
    return Math.max(20, columns - 7)
  }

  private updatePreview(): void {
    const text = this.buffer.replaceAll(/\s+/g, ' ').trim()
    if (text === '') {
      this.terminal.setPreview(undefined)
      return
    }
    const limit = this.lineWidthLimit()
    const visible = text.length > limit ? `…${text.slice(-(limit - 1))}` : text
    this.terminal.setPreview(`${this.agentLabel} ${visible}`)
  }
}

export function resetConnectionScopedRendering(
  renderer: DeltaRenderer,
  state: AgentRenderState | undefined,
  toolCalls: Map<string, ToolCallInfo>,
): AgentRenderState | undefined {
  renderer.discard()
  // Durable events replay after a reconnect; only what a delta announced can go stale.
  for (const [id, tool] of toolCalls) {
    if (tool.provisional === true) toolCalls.delete(id)
  }
  return state == null ? undefined : { text: state.text }
}
