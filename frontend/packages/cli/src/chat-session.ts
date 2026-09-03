import type {
  AgentEvent,
  AgentInteraction,
  InteractionAnswer,
  ListAgentEventsResponse,
  OmnaraClient,
} from '@omnara/sdk'
import { ApiError, openAgentEventStream, sdk } from '@omnara/sdk'
import { useCallback, useEffect, useMemo, useReducer, useState } from 'react'

import { type ChatState, initialChatState, reduceChat } from './chat-state.ts'

export interface ChatTarget {
  client: OmnaraClient
  orgId: string
  projectId: string
  agentId: string
}

interface AgentPath {
  orgID: string
  projectID: string
  agentID: string
}

const historyEventLimit = 500
const interactionPollMs = 1000

function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    return `HTTP ${error.status} (${error.code ?? 'no code'}): ${error.message}`
  }
  return error instanceof Error ? error.message : String(error)
}

function delay(ms: number, signal: AbortSignal): Promise<boolean> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve(false)
      return
    }
    const timer = setTimeout(() => {
      finish(true)
    }, ms)
    const onAbort = () => {
      finish(false)
    }
    function finish(completed: boolean) {
      clearTimeout(timer)
      signal.removeEventListener('abort', onAbort)
      resolve(completed)
    }
    signal.addEventListener('abort', onAbort, { once: true })
  })
}

async function loadHistory(
  client: OmnaraClient,
  path: AgentPath,
): Promise<{ events: AgentEvent[]; truncated: boolean }> {
  const events: AgentEvent[] = []
  let before: number | null = 0
  do {
    const { data }: { data: ListAgentEventsResponse } = await sdk.listEvents({
      client,
      path,
      query: { before_sequence: before, limit: 100 },
    })
    events.push(...data.data)
    before = data.next_before_sequence ?? null
  } while (before !== null && events.length < historyEventLimit)
  events.sort((a, b) => a.sequence - b.sequence)
  return { events, truncated: before !== null }
}

export interface ChatSession {
  state: ChatState
  send: (text: string) => Promise<void>
  answer: (interaction: AgentInteraction, answers: InteractionAnswer[]) => Promise<void>
}

export function useChatSession({ client, orgId, projectId, agentId }: ChatTarget): ChatSession {
  const [state, dispatch] = useReducer(reduceChat, undefined, initialChatState)
  const [initialSequence, setInitialSequence] = useState<number>()
  const path = useMemo<AgentPath>(
    () => ({ orgID: orgId, projectID: projectId, agentID: agentId }),
    [orgId, projectId, agentId],
  )

  useEffect(() => {
    const abort = new AbortController()
    void loadHistory(client, path).then(
      ({ events, truncated }) => {
        if (abort.signal.aborted) return
        dispatch({ type: 'history', events, truncated })
        setInitialSequence(events.at(-1)?.sequence ?? 0)
      },
      (error: unknown) => {
        if (abort.signal.aborted) return
        dispatch({ type: 'stream_failed', message: describeError(error) })
      },
    )
    return () => {
      abort.abort()
    }
  }, [client, path])

  useEffect(() => {
    if (initialSequence === undefined) return
    const abort = new AbortController()
    void (async () => {
      try {
        const frames = openAgentEventStream({
          client,
          path,
          query: { after_sequence: initialSequence, stream_deltas: true },
          signal: abort.signal,
          onConnectionStateChange: (connection) => {
            if (connection.state === 'reconnecting') dispatch({ type: 'reconnecting' })
          },
        })
        for await (const frame of frames) {
          dispatch({ type: 'frame', frame, now: Date.now() })
        }
      } catch (error) {
        if (abort.signal.aborted) return
        dispatch({ type: 'stream_failed', message: describeError(error) })
      }
    })()
    return () => {
      abort.abort()
    }
  }, [client, path, initialSequence])

  useEffect(() => {
    const abort = new AbortController()
    const seen = new Set<string>()
    let failing = false
    void (async () => {
      for (;;) {
        try {
          const { data } = await sdk.listAgentInteractions({
            client,
            path,
            query: { state: 'open' },
          })
          if (abort.signal.aborted) return
          failing = false
          const fresh = data.data.filter((interaction) => !seen.has(interaction.id))
          for (const interaction of fresh) seen.add(interaction.id)
          dispatch({ type: 'interactions', interactions: fresh })
        } catch (error) {
          if (abort.signal.aborted) return
          if (!failing) {
            failing = true
            dispatch({
              type: 'error',
              label: 'interactions',
              message: `could not check for open interactions: ${describeError(error)}`,
            })
          }
        }
        if (!(await delay(interactionPollMs, abort.signal))) return
      }
    })()
    return () => {
      abort.abort()
    }
  }, [client, path])

  const send = useCallback(
    async (text: string) => {
      dispatch({ type: 'sent', text, now: Date.now() })
      try {
        await sdk.createAgentInput({
          client,
          path,
          body: { content_blocks: [{ type: 'text', text }] },
        })
      } catch (error) {
        dispatch({ type: 'error', label: 'error', message: describeError(error) })
      }
    },
    [client, path],
  )

  const answer = useCallback(
    async (interaction: AgentInteraction, answers: InteractionAnswer[]) => {
      try {
        await sdk.resolveAgentInteraction({
          client,
          path: { ...path, interactionID: interaction.id },
          body: { answers },
        })
        dispatch({ type: 'answered', interaction, answers, now: Date.now() })
      } catch (error) {
        dispatch({ type: 'answer_failed', interaction, message: describeError(error) })
      }
    },
    [client, path],
  )

  return { state, send, answer }
}
