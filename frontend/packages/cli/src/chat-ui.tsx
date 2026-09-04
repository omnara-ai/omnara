import {
  type AgentChatScope,
  type AgentChatStatus,
  type AgentInputBacklogItem,
  backlogInputPreview,
  type OmnaraUIMessage,
  useAgentChat,
  useAgentInteractions,
  useResolveAgentInteraction,
} from '@omnara/react'
import * as schemas from '@omnara/sdk/zod'
import { Box, Static, Text, useApp } from 'ink'
import { useEffect, useMemo, useReducer, useState } from 'react'
import * as z from 'zod'

import { blockText, summaryWidth, toolCallSummary } from './agent-rendering.ts'
import { InteractionPrompt, Label, TextInput } from './chat-prompts.tsx'
import { abbreviate } from './output.ts'

type MessagePart = OmnaraUIMessage['parts'][number]
type ToolPart = Extract<MessagePart, { type: 'dynamic-tool' }>

const spinnerFrames = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏']
const streamingTailLines = 8
const streamingTailChars = 1500
const zToolInput = z.record(z.string(), z.json())
const zToolOutput = z.object({
  outcome: schemas.zToolCallOutcome,
  contentBlocks: z.array(schemas.zToolResultContentBlock),
})

function formatDuration(ms: number): string {
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
  const summary = useMemo(() => toolSummary(part), [part])
  const output = useMemo(() => toolOutput(part), [part])
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

function attachmentLabel(count: number): string {
  return count === 1 ? '[1 attachment]' : `[${String(count)} attachments]`
}

function MessageView({ message, live }: { message: OmnaraUIMessage; live: boolean }) {
  if (message.role === 'user') {
    const lines = message.parts.flatMap((part) =>
      part.type === 'text' && part.text.trim() !== '' ? [part.text] : [],
    )
    const attachmentCount = message.parts.filter((part) => part.type === 'data-media').length
    if (lines.length === 0 && attachmentCount === 0) return null
    return (
      <Box marginTop={1}>
        <Text>
          <Label name="you" color="cyan" />
          {lines.length > 0 && ` ${lines.join('\n')}`}
          {attachmentCount > 0 && <Text dimColor> {attachmentLabel(attachmentCount)}</Text>}
        </Text>
      </Box>
    )
  }
  return (
    <Box flexDirection="column">
      {message.parts.map((part) => (
        <Box key={part.id} marginTop={1}>
          <PartView part={part} live={live} />
        </Box>
      ))}
    </Box>
  )
}

interface Activity {
  text: string
  detail?: string
}

function currentActivity(
  status: AgentChatStatus,
  isWorking: boolean,
  live: OmnaraUIMessage[],
): Activity | undefined {
  if (status === 'submitted') return { text: 'sending…' }
  if (!isWorking) return undefined
  const assistant = live.filter((message) => message.role === 'assistant')
  const running = assistant.flatMap((message) =>
    message.parts.flatMap((part) =>
      part.type === 'dynamic-tool' && part.state !== 'output-available' ? [part.toolName] : [],
    ),
  )
  if (running.length > 0) return { text: `running ${running.join(', ')}…` }
  const last = assistant.at(-1)?.parts.at(-1)
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

function transitionWorkTimer(
  snapshot: WorkTimerSnapshot,
  phase: WorkTimerPhase,
): WorkTimerSnapshot {
  const now = Date.now()
  const { startedAt, accumulatedMs } = snapshot
  const elapsed = accumulatedMs + (startedAt == null ? 0 : now - startedAt)
  if (phase === 'working') {
    return startedAt != null ? snapshot : { startedAt: now, accumulatedMs }
  }
  if (phase === 'paused') {
    return startedAt == null ? snapshot : { accumulatedMs: elapsed }
  }
  if (startedAt == null && accumulatedMs === 0) return snapshot
  return { accumulatedMs: 0, lastDuration: elapsed }
}

function useWorkTimer(phase: WorkTimerPhase): WorkTimerSnapshot {
  const [snapshot, transition] = useReducer(transitionWorkTimer, { accumulatedMs: 0 })
  useEffect(() => {
    transition(phase)
  }, [phase])
  return snapshot
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
  const active = activity != null
  useEffect(() => {
    if (!active) return
    const interval = setInterval(() => {
      setTick((previous) => ({
        frame: (previous.frame + 1) % spinnerFrames.length,
        now: Date.now(),
      }))
    }, 120)
    return () => {
      clearInterval(interval)
    }
  }, [active])
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

function backlogState(input: AgentInputBacklogItem, queueIndex: number): string {
  if (input.delivery_mode === 'steering') return 'sending'
  if (input.delivery_mode === 'optimistic') return 'pending'
  return queueIndex === 0 ? 'next' : `queued #${String(queueIndex + 1)}`
}

function BacklogView({ inputs }: { inputs: AgentInputBacklogItem[] }) {
  const queueIndexes = new Map<string, number>()
  for (const input of inputs) {
    if (input.delivery_mode !== 'steering') queueIndexes.set(input.id, queueIndexes.size)
  }
  return (
    <Box flexDirection="column">
      {inputs.map((input) => (
        <Box key={input.id} marginTop={1}>
          <Text>
            <Label name="you" color="cyan" />{' '}
            <Text dimColor>({backlogState(input, queueIndexes.get(input.id) ?? 0)})</Text>{' '}
            {backlogInputPreview(input).text}
          </Text>
        </Box>
      ))}
    </Box>
  )
}

function ErrorLines({ errors }: { errors: (Error | { error: string } | null | undefined)[] }) {
  const messages = new Set(
    errors.flatMap((error) =>
      error == null ? [] : [error instanceof Error ? error.message : error.error],
    ),
  )
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
  const chat = useAgentChat(scope, { source: 'cli' })
  const interactions = useAgentInteractions(
    scope.orgID,
    scope.projectID,
    scope.agentID,
    chat.isWorking,
  )
  const resolveInteraction = useResolveAgentInteraction(scope.orgID, scope.projectID, scope.agentID)
  const { exit } = useApp()
  const [draft, setDraft] = useState('')

  const ready = chat.historyStatus === 'success'
  const showOlder = chat.hasOlderMessages && ready
  const [settled, live] = useMemo(
    () => splitLive(chat.messages, chat.isWorking),
    [chat.messages, chat.isWorking],
  )
  const transcript = useMemo<TranscriptItem[]>(
    () => [
      ...(showOlder ? [{ kind: 'older' as const }] : []),
      ...settled.map((message) => ({ kind: 'message' as const, message })),
    ],
    [settled, showOlder],
  )
  const interaction = interactions.data?.data[0]
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
      <ErrorLines
        errors={[chat.historyError, chat.error, interactions.error, resolveInteraction.error]}
      />
      <Box marginTop={1}>
        <StatusLine activity={activity} timer={timer} />
      </Box>
      {interaction != null ? (
        <InteractionPrompt
          key={interaction.id}
          interaction={interaction}
          onAnswer={(answers) => {
            void resolveInteraction
              .mutateAsync({ interactionID: interaction.id, body: { answers } })
              .catch(() => undefined)
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
