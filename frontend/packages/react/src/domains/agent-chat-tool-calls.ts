import type { ToolCallUpdate, ToolResultEvent } from '@omnara/sdk'

import type { ToolCallUpdateListener } from './agent-chat-types'

/**
 * Who is listening for tool calls changing state in a conversation. Live frames are passed
 * on as they come. They are not replayed, so a call that ended while the stream was down is
 * reported from its durable result instead.
 */
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

  completed(result: ToolResultEvent): void {
    this.emit({
      org_id: result.org_id,
      project_id: result.project_id,
      agent_id: result.agent_id,
      agent_profile_id: result.agent_profile_id,
      parent_agent_id: result.parent_agent_id,
      root_agent_id: result.root_agent_id,
      tool_call_id: result.tool_call_id,
      state: 'completed',
    })
  }
}
