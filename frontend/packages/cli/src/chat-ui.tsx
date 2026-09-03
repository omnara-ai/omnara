import {
  type AgentChatScope,
  type AgentInputBacklogItem,
  type OmnaraUIMessage,
  useAgentChat,
  useAgentInteractions,
  useResolveAgentInteraction,
} from '@omnara/react'
import type { InteractionAnswer } from '@omnara/sdk'
import * as schemas from '@omnara/sdk/zod'
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
  outcome: schemas.zToolCallOutcome,
  contentBlocks: z.array(schemas.zToolResultContentBlock),
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
  for (const block of output.data.contentBlocks) {
    if (block.type !== 'text' && block.type !== 'structured_data') continue
    const text = blockText(block)
    if (text.trim() !== '') return { outcome: output.data.outcome, text }
  }
  return { outcome: output.data.outcome, text: undefined }
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
    const lines: string[] = []
    for (const part of message.parts) {
      if (part.type === 'text' && part.text.trim() !== '') lines.push(part.text)
    }
    if (lines.length === 0) return null
    const text = lines.join('\n')
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
  const parts: MessagePart[] = []
  const running: string[] = []
  for (const message of live) {
    if (message.role !== 'assistant') continue
    for (const part of message.parts) {
      parts.push(part)
      if (part.type === 'dynamic-tool' && part.state !== 'output-available') {
        running.push(part.toolName)
      }
    }
  }
  if (running.length > 0) return { text: `running ${running.join(', ')}…` }
  const last = parts.at(-1)
  if (last?.type === 'reasoning') return { text: 'thinking…', detail: last.text.slice(-200) }
  if (last?.type === 'data-thinking' && last.data.active) return { text: 'thinking…' }
  if (last?.type === 'text' && last.state === 'streaming') return { text: 'writing…' }
  return { text: 'working…' }
}

interface WorkTimerSnapshot {
  startedAt?: number
  accumulatedMs: number
  lastDuration?: number
}

type WorkTimerPhase = 'working' | 'paused' | 'idle'

class WorkTimer {
  private listeners = new Set<() => void>()
  private snapshot: WorkTimerSnapshot = { accumulatedMs: 0 }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  getSnapshot = (): WorkTimerSnapshot => this.snapshot

  setPhase(phase: WorkTimerPhase): void {
    const now = Date.now()
    const { startedAt, accumulatedMs } = this.snapshot
    const elapsed = accumulatedMs + (startedAt == null ? 0 : now - startedAt)
    if (phase === 'working') {
      if (startedAt != null) return
      this.snapshot = { startedAt: now, accumulatedMs }
    } else if (phase === 'paused') {
      if (startedAt == null) return
      this.snapshot = { accumulatedMs: elapsed }
    } else {
      if (startedAt == null && accumulatedMs === 0) return
      this.snapshot = { accumulatedMs: 0, lastDuration: elapsed }
    }
    for (const listener of this.listeners) listener()
  }
}

function useWorkTimer(phase: WorkTimerPhase): WorkTimerSnapshot {
  const [timer] = useState(() => new WorkTimer())
  useEffect(() => {
    timer.setPhase(phase)
  }, [phase, timer])
  return useSyncExternalStore(timer.subscribe, timer.getSnapshot, timer.getSnapshot)
}

function StatusLine({
  activity,
  timer,
}: {
  activity: Activity | undefined
  timer: WorkTimerSnapshot
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
      {timer.startedAt != null && tick.now != null && (
        <Text dimColor> ({formatDuration(timer.accumulatedMs + tick.now - timer.startedAt)})</Text>
      )}
      {activity.detail != null && <Text dimColor> {abbreviate(activity.detail, 60)}</Text>}
    </Text>
  )
}

function backlogText(input: AgentInputBacklogItem): string {
  if (input.delivery_mode === 'optimistic') {
    return input.text.trim() !== '' ? input.text : `${String(input.attachmentCount)} attachment(s)`
  }
  const lines: string[] = []
  let attachments = 0
  for (const block of input.content_blocks ?? []) {
    if (block.metadata?.omnara_hidden === 'true') continue
    if (block.type === 'media_ref') attachments += 1
    if (block.type !== 'text') continue
    const display = block.metadata?.omnara_display_text
    const text = (typeof display === 'string' ? display : block.text).trim()
    if (text !== '') lines.push(text)
  }
  if (lines.length > 0) return lines.join('\n')
  return attachments === 0 ? 'Message' : `${String(attachments)} attachment(s)`
}

function backlogState(input: AgentInputBacklogItem, queueIndex: number): string {
  if (input.delivery_mode === 'steering') return 'sending'
  if (input.delivery_mode === 'optimistic') return 'pending'
  return queueIndex === 0 ? 'next' : `queued #${String(queueIndex + 1)}`
}

function BacklogView({ inputs }: { inputs: AgentInputBacklogItem[] }) {
  const waiting = inputs.filter((input) => input.delivery_mode !== 'steering')
  return (
    <Box flexDirection="column">
      {inputs.map((input) => (
        <Box key={input.id} marginTop={1}>
          <Text>
            <Label name="you" color="cyan" />{' '}
            <Text dimColor>({backlogState(input, waiting.indexOf(input))})</Text>{' '}
            {backlogText(input)}
          </Text>
        </Box>
      ))}
    </Box>
  )
}

function ErrorLines({ errors }: { errors: unknown[] }) {
  const messages = new Set<string>()
  for (const error of errors) {
    const message = errorMessage(error)
    if (message != null) messages.add(message)
  }
  return (
    <>
      {[...messages].map((message) => (
        <Text key={message}>
          <Label name="error" color="red" /> {message}
        </Text>
      ))}
    </>
  )
}

function Composer({
  draft,
  onDraftChange,
  onSend,
  onQuit,
}: {
  draft: string
  onDraftChange: (value: string) => void
  onSend: (text: string) => void
  onQuit: () => void
}) {
  return (
    <Box>
      <Text bold color="cyan">
        ❯{' '}
      </Text>
      <TextInput
        value={draft}
        onChange={onDraftChange}
        onSubmit={(line) => {
          const trimmed = line.trim()
          onDraftChange('')
          if (trimmed === '') return
          if (trimmed === '/quit' || trimmed === '/exit') {
            onQuit()
            return
          }
          onSend(trimmed)
        }}
      />
    </Box>
  )
}

type TranscriptItem = { kind: 'older' } | { kind: 'message'; message: OmnaraUIMessage }

function splitLive(
  messages: OmnaraUIMessage[],
  isWorking: boolean,
): [OmnaraUIMessage[], OmnaraUIMessage[]] {
  if (!isWorking) return [messages, []]
  const currentTurn = messages.at(-1)?.metadata?.turnId
  let start = messages.length
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index]
    if (message == null || message.metadata?.turnId !== currentTurn) break
    if (message.role === 'assistant') start = index
  }
  return [messages.slice(0, start), messages.slice(start)]
}

function usePendingInteraction(scope: AgentChatScope, isWorking: boolean) {
  const interactions = useAgentInteractions(scope.orgID, scope.projectID, scope.agentID, isWorking)
  const resolveInteraction = useResolveAgentInteraction(scope.orgID, scope.projectID, scope.agentID)
  const [answered, setAnswered] = useState<ReadonlySet<string>>(new Set())
  const interaction = (interactions.data?.data ?? []).find((item) => !answered.has(item.id))
  return {
    interaction,
    errors: [interactions.error, resolveInteraction.error],
    answer(interactionID: string, answers: InteractionAnswer[]) {
      setAnswered((current) => new Set(current).add(interactionID))
      resolveInteraction.mutateAsync({ interactionID, body: { answers } }).catch(() => {
        setAnswered((current) => {
          const next = new Set(current)
          next.delete(interactionID)
          return next
        })
      })
    },
  }
}

function Transcript({ items, live }: { items: TranscriptItem[]; live: OmnaraUIMessage[] }) {
  return (
    <>
      <Static items={items}>
        {(item) =>
          item.kind === 'older' ? (
            <Text key="older" dimColor>
              (older history omitted)
            </Text>
          ) : (
            <MessageView key={item.message.id} message={item.message} live={false} />
          )
        }
      </Static>
      {live.map((message) => (
        <MessageView key={message.id} message={message} live />
      ))}
    </>
  )
}

export function Chat({ scope }: { scope: AgentChatScope }) {
  const chat = useAgentChat(scope)
  const pending = usePendingInteraction(scope, chat.isWorking)
  const { exit } = useApp()
  const [draft, setDraft] = useState('')

  const ready = chat.historyStatus === 'success'
  const [settled, live] = splitLive(chat.messages, chat.isWorking)
  const transcript: TranscriptItem[] = [
    ...(chat.hasOlderMessages && ready ? [{ kind: 'older' as const }] : []),
    ...settled.map((message) => ({ kind: 'message' as const, message })),
  ]
  const { interaction } = pending
  const activity =
    interaction == null ? currentActivity(chat.status, chat.isWorking, live) : undefined
  const timer = useWorkTimer(interaction != null ? 'paused' : chat.isWorking ? 'working' : 'idle')
  const lastDuration = interaction == null && !chat.isWorking ? timer.lastDuration : undefined

  return (
    <Box flexDirection="column">
      <Transcript items={transcript} live={live} />
      <BacklogView inputs={chat.inputBacklog.inputs} />
      {chat.historyStatus === 'pending' && <Text dimColor>loading history…</Text>}
      {lastDuration != null && lastDuration >= 1000 && (
        <Box marginTop={1}>
          <Text dimColor>✔ Worked for {formatDuration(lastDuration)}</Text>
        </Box>
      )}
      <ErrorLines errors={[chat.error, ...pending.errors]} />
      <Box marginTop={1}>
        <StatusLine activity={activity} timer={timer} />
      </Box>
      {interaction != null ? (
        <InteractionPrompt
          key={interaction.id}
          interaction={interaction}
          onAnswer={(answers) => {
            pending.answer(interaction.id, answers)
          }}
        />
      ) : (
        ready && (
          <Composer
            draft={draft}
            onDraftChange={setDraft}
            onQuit={exit}
            onSend={(text) => {
              void chat.sendMessage({ text }).catch(() => undefined)
            }}
          />
        )
      )}
    </Box>
  )
}
