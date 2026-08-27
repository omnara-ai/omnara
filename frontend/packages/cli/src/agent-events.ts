import type { AgentEventStreamData, OmnaraClient } from '@omnara/sdk'
import { AgentEventStreamError, openAgentEventStream } from '@omnara/sdk'

import { sleepSeconds } from './poll.ts'

export interface FollowAgentEventsOptions {
  client: OmnaraClient
  path: { orgID: string; projectID: string; agentID: string }
  afterSequence: number
  streamDeltas: boolean
  signal: AbortSignal
  onReconnect?: () => void
}

export async function* followAgentEvents(
  options: FollowAgentEventsOptions,
): AsyncGenerator<AgentEventStreamData> {
  let afterSequence = options.afterSequence
  while (!options.signal.aborted) {
    try {
      const { stream } = await openAgentEventStream({
        client: options.client,
        path: options.path,
        query: { after_sequence: afterSequence, stream_deltas: options.streamDeltas },
        signal: options.signal,
      })
      for await (const frame of stream) {
        if ('event_kind' in frame) afterSequence = Math.max(afterSequence, frame.sequence)
        yield frame
      }
    } catch (error) {
      if (error instanceof AgentEventStreamError && error.kind === 'aborted') return
      if (error instanceof AgentEventStreamError && error.retryable) {
        options.onReconnect?.()
        await sleepSeconds(1)
        continue
      }
      throw error
    }
  }
}
