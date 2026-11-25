import type { ToolCallUpdate } from '@omnara/sdk'

import type { ToolCallUpdateListener } from './agent-chat-types'

/** Who is listening for tool calls changing state on a conversation's live stream. */
export class ToolCallUpdates {
  private listeners = new Set<ToolCallUpdateListener>()

  subscribe = (listener: ToolCallUpdateListener): (() => void) => {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  emit(update: ToolCallUpdate): void {
    for (const listener of this.listeners) listener(update)
  }
}
