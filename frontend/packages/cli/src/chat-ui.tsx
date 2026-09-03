import {
  type AgentChatScope,
  type OmnaraUIMessage,
  useAgentChat,
  useAgentInteractions,
  useResolveAgentInteraction,
} from '@omnara/react'
import { Box, Static, Text, useApp } from 'ink'
import { useEffect, useState, useSyncExternalStore } from 'react'
import * as z from 'zod'

import { blockText, summaryWidth, toolCallSummary } from './agent-rendering.ts'
import { InteractionPrompt, Label, TextInput } from './chat-prompts.tsx'
import { abbreviate } from './output.ts'

type MessagePart = OmnaraUIMessage['parts'][number]
type ToolPart = Extract<MessagePart, { type: 'dynamic-tool' }>

const spinnerFrames = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏']
const streamingTailLines = 8
const streamingTailChars = 1500
const zToolInput = z.record(z.string(), z.unknown())
const zErrorBody = z.object({ error: z.string() })

function errorMessage(error: unknown): string | undefined {
  if (error == null) return undefined
  if (error instanceof Error) return error.message
  const body = zErrorBody.safeParse(error)
  if (body.success) return body.data.error
  return typeof error === 'string' ? error : 'unknown error'
}
const zToolOutput = z.object({
  outcome: z.string(),
  contentBlocks: z.array(z.object({ type: z.string(), text: z.string().optional() })),
})

export function formatDuration(ms: number): string {
  const seconds = Math.max(1, Math.round(ms / 1000))
  if (seconds < 60) return `${seconds}s`
  return `${Math.floor(seconds / 60)}m ${String(seconds % 60).padStart(2, '0')}s`
}

function outcomeColor(outcome: string): string {
  if (outcome === 'succeeded') return 'green'
  return outcome === 'canceled' ? 'yellow' : 'red'
}

function toolSummary(part: ToolPart): string | undefined {
  const input = zToolInput.safeParse(part.input)
  return input.success ? toolCallSummary(part.toolName, input.data) : undefined
}

function toolOutput(part: ToolPart): { outcome: string; text: string | undefined } | undefined {
  if (part.state !== 'output-available') return undefined
  const output = zToolOutput.safeParse(part.output)
  if (!output.success) return undefined
  const text = output.data.contentBlocks
    .filter((block) => block.type === 'text' || block.type === 'structured_data')
    .map(blockText)
    .find((line) => line.trim() !== '')
  return { outcome: output.data.outcome, text }
}

function ToolPartView({ part }: { part: ToolPart }) {
  const summary = toolSummary(part)
  const output = toolOutput(part)
  return (
    <Box flexDirection="column">
      <Text>
        <Label name="tool" color="magenta" /> {part.toolName}{' '}
        {output != null ? (
          <Text color={outcomeColor(output.outcome)}>({output.outcome})</Text>
        ) : part.state === 'output-error' ? (
          <Text color="red">(error)</Text>
        ) : (
          <Text dimColor>(running)</Text>
        )}
      </Text>
      {summary != null && <Text dimColor>{`  ${summary}`}</Text>}
      {output?.text != null && (
        <Text dimColor>{`  → ${abbreviate(output.text, summaryWidth)}`}</Text>
      )}
      {part.state === 'output-error' && <Text color="red">{`  ${part.errorText}`}</Text>}
    </Box>
  )
}

function clipTail(text: string): string {
  const lines = text.slice(-streamingTailChars).split('\n')
  const clipped = text.length > streamingTailChars || lines.length > streamingTailLines
  return `${clipped ? '…' : ''}${lines.slice(-streamingTailLines).join('\n')}`
}

function PartView({ part, live }: { part: MessagePart; live: boolean }) {
  switch (part.type) {
    case 'text':
      if (part.text.trim() === '') return null
      return (
        <Text>
          <Label name="agent" color="green" /> {live ? clipTail(part.text) : part.text}
        </Text>
      )
    case 'dynamic-tool':
      return <ToolPartView part={part} />
    case 'data-model-error':
      return (
        <Text>
          <Label name="error" color="red" /> {part.data.text}
        </Text>
      )
    case 'data-agent-config':
      return <Text dimColor>agent config {part.data.action}</Text>
    case 'data-media':
      return (
        <Text dimColor>[media{part.data.filename != null ? ` ${part.data.filename}` : ''}]</Text>
      )
    default:
      return null
  }
}

function MessageView({ message, live }: { message: OmnaraUIMessage; live: boolean }) {
  if (message.role === 'user') {
    const text = message.parts
      .map((part) => (part.type === 'text' ? part.text : ''))
      .filter((line) => line.trim() !== '')
      .join('\n')
    if (text === '') return null
    return (
      <Box marginTop={1}>
        <Text>
          <Label name="you" color="cyan" /> {text}
        </Text>
      </Box>
    )
  }
  return (
    <Box flexDirection="column">
      {message.parts.map((part) => {
        const view = <PartView part={part} live={live} />
        return (
          <Box key={part.id} marginTop={1}>
            {view}
          </Box>
        )
      })}
    </Box>
  )
}

interface Activity {
  text: string
  detail?: string
}

function currentActivity(
  status: ReturnType<typeof useAgentChat>['status'],
  isWorking: boolean,
  live: OmnaraUIMessage[],
): Activity | undefined {
  if (status === 'submitted') return { text: 'sending…' }
  if (!isWorking) return undefined
  const parts = live.filter((message) => message.role === 'assistant').flatMap((m) => m.parts)
  const running = parts.filter(
    (part): part is ToolPart => part.type === 'dynamic-tool' && part.state !== 'output-available',
  )
  if (running.length > 0) {
    const names = running.map((part) => part.toolName).join(', ')
    return { text: `running ${names}…` }
  }
  const last = parts.at(-1)
  if (last?.type === 'reasoning') return { text: 'thinking…', detail: last.text.slice(-200) }
  if (last?.type === 'data-thinking' && last.data.active) return { text: 'thinking…' }
  if (last?.type === 'text' && last.state === 'streaming') return { text: 'writing…' }
  return { text: 'working…' }
}

interface WorkTimerSnapshot {
  startedAt?: number
  lastDuration?: number
}

class WorkTimer {
  private listeners = new Set<() => void>()
  private snapshot: WorkTimerSnapshot = {}

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  getSnapshot = (): WorkTimerSnapshot => this.snapshot

  setActive(active: boolean): void {
    const now = Date.now()
    if (active) {
      if (this.snapshot.startedAt != null) return
      this.snapshot = { startedAt: now }
    } else {
      if (this.snapshot.startedAt == null) return
      this.snapshot = { lastDuration: now - this.snapshot.startedAt }
    }
    for (const listener of this.listeners) listener()
  }
}

function useWorkTimer(active: boolean): WorkTimerSnapshot {
  const [timer] = useState(() => new WorkTimer())
  useEffect(() => {
    timer.setActive(active)
  }, [active, timer])
  return useSyncExternalStore(timer.subscribe, timer.getSnapshot, timer.getSnapshot)
}

function StatusLine({
  activity,
  startedAt,
}: {
  activity: Activity | undefined
  startedAt: number | undefined
}) {
  const [tick, setTick] = useState<{ frame: number; now: number | undefined }>({
    frame: 0,
    now: undefined,
  })
  useEffect(() => {
    if (activity == null) return
    const timer = setInterval(() => {
      setTick((previous) => ({
        frame: (previous.frame + 1) % spinnerFrames.length,
        now: Date.now(),
      }))
    }, 120)
    return () => {
      clearInterval(timer)
    }
  }, [activity])
  if (activity == null) return null
  return (
    <Text wrap="truncate-end">
      <Text color="cyan">{spinnerFrames[tick.frame]}</Text> {activity.text}
      {startedAt != null && tick.now != null && (
        <Text dimColor> ({formatDuration(tick.now - startedAt)})</Text>
      )}
      {activity.detail != null && <Text dimColor> {abbreviate(activity.detail, 60)}</Text>}
    </Text>
  )
}

function splitLive(
  messages: OmnaraUIMessage[],
  isWorking: boolean,
): [OmnaraUIMessage[], OmnaraUIMessage[]] {
  if (!isWorking) return [messages, []]
  let lastAssistant = messages.length - 1
  while (lastAssistant >= 0 && messages[lastAssistant]?.role !== 'assistant') lastAssistant -= 1
  if (lastAssistant < 0) return [messages, []]
  return [messages.slice(0, lastAssistant), messages.slice(lastAssistant)]
}

export function Chat({ scope }: { scope: AgentChatScope }) {
  const chat = useAgentChat(scope)
  const interactions = useAgentInteractions(
    scope.orgID,
    scope.projectID,
    scope.agentID,
    chat.isWorking,
  )
  const resolveInteraction = useResolveAgentInteraction(scope.orgID, scope.projectID, scope.agentID)
  const { exit } = useApp()
  const [draft, setDraft] = useState('')
  const [answered, setAnswered] = useState<ReadonlySet<string>>(new Set())

  const [settled, live] = splitLive(chat.messages, chat.isWorking)
  const interaction = (interactions.data?.data ?? []).find((item) => !answered.has(item.id))
  const activity =
    interaction == null ? currentActivity(chat.status, chat.isWorking, live) : undefined
  const timer = useWorkTimer(chat.isWorking && interaction == null)
  const ready = chat.historyStatus === 'success'
  const errors = [chat.error, interactions.error, resolveInteraction.error]
    .map(errorMessage)
    .filter((message) => message != null)

  return (
    <Box flexDirection="column">
      <Static items={settled}>
        {(message) => <MessageView key={message.id} message={message} live={false} />}
      </Static>
      {live.map((message) => (
        <MessageView key={message.id} message={message} live />
      ))}
      {!ready && chat.historyStatus === 'pending' && <Text dimColor>loading history…</Text>}
      {chat.hasOlderMessages && ready && <Text dimColor>(older history omitted)</Text>}
      {activity == null && timer.lastDuration != null && timer.lastDuration >= 1000 && (
        <Box marginTop={1}>
          <Text dimColor>✔ Worked for {formatDuration(timer.lastDuration)}</Text>
        </Box>
      )}
      {errors.map((message, index) => (
        <Text key={index}>
          <Label name="error" color="red" /> {message}
        </Text>
      ))}
      <Box marginTop={1}>
        <StatusLine activity={activity} startedAt={timer.startedAt} />
      </Box>
      {interaction != null ? (
        <InteractionPrompt
          key={interaction.id}
          interaction={interaction}
          onAnswer={(answers) => {
            setAnswered((current) => new Set(current).add(interaction.id))
            resolveInteraction
              .mutateAsync({ interactionID: interaction.id, body: { answers } })
              .catch(() => {
                setAnswered((current) => {
                  const next = new Set(current)
                  next.delete(interaction.id)
                  return next
                })
              })
          }}
        />
      ) : (
        ready && (
          <Box>
            <Text bold color="cyan">
              ❯{' '}
            </Text>
            <TextInput
              value={draft}
              onChange={setDraft}
              onSubmit={(line) => {
                const trimmed = line.trim()
                setDraft('')
                if (trimmed === '') return
                if (trimmed === '/quit' || trimmed === '/exit') {
                  exit()
                  return
                }
                void chat.sendMessage({ text: trimmed }).catch(() => undefined)
              }}
            />
          </Box>
        )
      )}
    </Box>
  )
}
